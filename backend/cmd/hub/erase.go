package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/telemetry"
)

// Exit codes of hub erase-subject.
const (
	eraseOK       = 0
	eraseFailed   = 1
	eraseUsage    = 2
	eraseDeferred = 3 // done, but some requests were still in flight: run again later
)

const eraseUsageText = `usage: hub erase-subject -tenant ID -actor WHO -reason TEXT [-since RFC3339] [-until RFC3339] [-dry-run] < subject

Purges every stored body of the tenant's requests that hold the subject identifier (DESIGN §16.5),
keeping hashes, sizes, timelines and metadata, and records the deletion in change_log.
The identifier is read from the first line of stdin, never from flags (process listings, shell
history). Environment: HUB_PG_DSN and HUB_AUDIT_MASK_KEY (the gateway's mask key) are required;
the seal key file (-dev-key-file, HUB_DEV_KEY_FILE) is needed when the tenant has sealed payloads.
Prints the result as JSON. Exit 0 done, 3 done with deferred requests (still queued or with
callbacks to deliver: run again once they are terminal), 1 failed (nothing changed), 2 usage.`

// eraseConfig is the parsed command line.
type eraseConfig struct {
	req     control.EraseRequest
	dsn     string
	maskKey string
	keyFile string
}

func parseErase(args []string, getenv func(string) string, stdin io.Reader, stderr io.Writer) (eraseConfig, error) {
	c := eraseConfig{dsn: getenv("HUB_PG_DSN"), maskKey: getenv("HUB_AUDIT_MASK_KEY")}
	defKeyFile := getenv("HUB_DEV_KEY_FILE")
	if defKeyFile == "" {
		defKeyFile = ".data/dev.key"
	}
	var since, until string
	fs := flag.NewFlagSet("hub erase-subject", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, eraseUsageText); fs.PrintDefaults() }
	fs.StringVar(&c.req.Tenant, "tenant", "", "tenant whose requests are searched (required)")
	fs.StringVar(&c.req.Actor, "actor", "", "who runs the erasure, recorded in change_log (required)")
	fs.StringVar(&c.req.Reason, "reason", "", "why, citing the erasure ticket (required, >= 10 characters)")
	fs.StringVar(&since, "since", "", "only requests created at or after this time (RFC 3339)")
	fs.StringVar(&until, "until", "", "only requests created before this time (RFC 3339)")
	fs.BoolVar(&c.req.DryRun, "dry-run", false, "report what would be erased; change nothing")
	fs.StringVar(&c.keyFile, "dev-key-file", defKeyFile, "seal key file (sealed payloads)")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	var errs []error
	if fs.NArg() > 0 {
		errs = append(errs, fmt.Errorf("unexpected arguments (the subject is read from stdin): %d given", fs.NArg()))
	}
	for _, t := range []struct {
		name, v string
		dst     *time.Time
	}{{"since", since, &c.req.Since}, {"until", until, &c.req.Until}} {
		if t.v == "" {
			continue
		}
		v, err := time.Parse(time.RFC3339, t.v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: want RFC 3339, got %q", t.name, t.v))
		}
		*t.dst = v
	}
	if c.dsn == "" {
		errs = append(errs, errors.New("HUB_PG_DSN is required (environment only)"))
	}
	if len(c.maskKey) < 32 {
		errs = append(errs, errors.New("HUB_AUDIT_MASK_KEY is required (the gateway's key, >= 32 bytes): masked payloads cannot be found without it"))
	}
	line, err := bufio.NewReader(io.LimitReader(stdin, 4096)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		errs = append(errs, fmt.Errorf("read subject from stdin: %w", err))
	}
	c.req.Subject = strings.TrimSpace(line)
	if c.req.Subject == "" {
		errs = append(errs, errors.New("no subject identifier on stdin"))
	}
	return c, errors.Join(errs...)
}

// eraseSubjectCmd runs hub erase-subject.
func eraseSubjectCmd(ctx context.Context, args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) int {
	c, err := parseErase(args, getenv, stdin, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return eraseUsage
	}
	if err != nil {
		fmt.Fprintf(stderr, "hub erase-subject: %v\n\n%s\n", err, eraseUsageText)
		return eraseUsage
	}
	// The identifier and the keys are redacted by value from every log line (defence in depth:
	// nothing below logs them).
	secrets := telemetry.NewSecretSet(c.req.Subject, c.maskKey)
	if u, err := url.Parse(c.dsn); err == nil && u.User != nil {
		if pw, ok := u.User.Password(); ok {
			secrets.Add(pw)
		}
	}
	log := telemetry.NewLogger(stderr, slog.LevelInfo, secrets, "erase-subject")

	cfg, err := pgxpool.ParseConfig(c.dsn)
	if err != nil {
		fmt.Fprintln(stderr, "hub erase-subject: HUB_PG_DSN does not parse")
		return eraseUsage
	}
	cfg.MaxConns = 2
	cfg.ConnConfig.RuntimeParams["application_name"] = "hub-erase-subject"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		log.ErrorContext(ctx, "erase-subject: connect", slog.String("error", err.Error()))
		return eraseFailed
	}
	defer pool.Close()

	e := &control.Erasure{Pool: pool, Masker: audit.NewMasker([]byte(c.maskKey)), Sealer: loadSealer(c.keyFile)}
	res, err := e.EraseSubject(ctx, c.req)
	if errors.Is(err, control.ErrInvalidErasure) {
		fmt.Fprintf(stderr, "hub erase-subject: %v\n", err)
		return eraseUsage
	}
	if err != nil {
		log.ErrorContext(ctx, "erase-subject failed, nothing changed", slog.String("tenant", c.req.Tenant), slog.String("error", err.Error()))
		return eraseFailed
	}
	log.InfoContext(ctx, "erase-subject finished", slog.String("tenant", res.Tenant), slog.String("change_id", res.ChangeID),
		slog.Bool("dry_run", res.DryRun), slog.Int("erased", len(res.Erased)), slog.Int("deferred", len(res.Deferred)),
		slog.Int64("events", res.Events), slog.Int64("results", res.Results), slog.Int64("dead_letters", res.DeadLetters),
		slog.Int64("callbacks", res.Callbacks), slog.Int64("sessions", res.Sessions))
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(res); err != nil {
		return eraseFailed
	}
	if len(res.Deferred) > 0 {
		return eraseDeferred
	}
	return eraseOK
}

// loadSealer returns the sealed-policy key source (the same derivation as the roles, HUB-208),
// or nil without a key file: the erasure then fails if the tenant has sealed payloads.
func loadSealer(path string) *audit.Sealer {
	master, err := creds.ReadKeyFile(path)
	if err != nil {
		return nil
	}
	defer clear(master)
	keys, err := audit.NewLocalKeys(master)
	if err != nil {
		return nil
	}
	return audit.NewSealer(keys)
}

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
)

// hubctl creds manages the local dev credential file (HUB-084, DESIGN §9). Values are written
// encrypted and are never printed: list shows key names only.

//nolint:gosec // G101 false positive: usage text, no credential
const credsUsage = `usage: hubctl creds <command> [flags]

commands:
  init-key [-key-file PATH] [-force]
      Write a new random 32-byte dev key (base64, mode 0600). Refuses to overwrite unless -force.
  set [-file PATH] [-key-file PATH] [-slot active|next] [-version N] ACCOUNT KEY=VALUE...
      Replace one slot of ACCOUNT with the given values. The version defaults to the
      account's highest version + 1. Values must be at least 6 bytes (redaction minimum).
  list [-file PATH] [-key-file PATH]
      Print accounts, slots, versions and key names. Never prints values.

defaults: -key-file $HUB_DEV_KEY_FILE or .data/dev.key; -file is the path of a
localfile:// HUB_CRED_SOURCE, else .data/dev-secrets.enc. Both are git-ignored and must stay
out of config/: every file there is packed into snapshot blobs.`

const minValueLen = 6 // telemetry.SecretSet ignores shorter values, so they would not be redacted

func credsCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, credsUsage)
		return 2
	}
	switch args[0] {
	case "init-key":
		return credsInitKey(args[1:], stdout, stderr)
	case "set":
		return credsSet(args[1:], stdout, stderr)
	case "list":
		return credsList(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, credsUsage)
		return 2
	}
}

func defaultKeyFile() string {
	if p := os.Getenv(creds.EnvDevKeyFile); p != "" {
		return p
	}
	return ".data/dev.key"
}

func defaultSecretsFile() string {
	if p, ok := strings.CutPrefix(os.Getenv("HUB_CRED_SOURCE"), "localfile://"); ok && p != "" {
		return p
	}
	return ".data/dev-secrets.enc"
}

func credsFlags(name string, stderr io.Writer) (*flag.FlagSet, *string, *string) {
	fs := flag.NewFlagSet("hubctl creds "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", defaultSecretsFile(), "encrypted secrets file")
	keyFile := fs.String("key-file", defaultKeyFile(), "dev key file")
	return fs, file, keyFile
}

func credsInitKey(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("hubctl creds init-key", flag.ContinueOnError)
	fs.SetOutput(stderr)
	keyFile := fs.String("key-file", defaultKeyFile(), "dev key file to create")
	force := fs.Bool("force", false, "overwrite an existing key (the old encrypted file becomes unreadable)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return 2
	}
	key, err := creds.GenerateKey()
	if err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	if err := creds.WriteKeyFile(*keyFile, key, *force); err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote dev key %s (mode 0600); export %s=%s\n", *keyFile, creds.EnvDevKeyFile, *keyFile)
	return 0
}

// openStore opens the encrypted file; a missing file reads as empty so set can create it.
func openStore(file, keyFile string, stderr io.Writer) (*creds.LocalFile, map[string]creds.AccountSecrets, bool) {
	lf, err := creds.OpenLocalFile(file, keyFile)
	if err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return nil, nil, false
	}
	all, err := lf.ReadAll()
	if creds.IsNotExist(err) {
		return lf, map[string]creds.AccountSecrets{}, true
	}
	if err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return nil, nil, false
	}
	return lf, all, true
}

func credsSet(args []string, stdout, stderr io.Writer) int {
	fs, file, keyFile := credsFlags("set", stderr)
	slot := fs.String("slot", "active", "slot to replace: active or next")
	version := fs.Int("version", 0, "slot version (default: highest existing version + 1)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 2 || (*slot != "active" && *slot != "next") {
		fmt.Fprintln(stderr, credsUsage)
		return 2
	}
	account := fs.Arg(0)
	values := map[string]string{}
	for _, kv := range fs.Args()[1:] {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			fmt.Fprintf(stderr, "hubctl: want KEY=VALUE, got an argument without '=' for account %s\n", account)
			return 2
		}
		if len(v) < minValueLen {
			fmt.Fprintf(stderr, "hubctl: value of %s is shorter than %d bytes and could not be redacted from logs\n", k, minValueLen)
			return 2
		}
		values[k] = v
	}
	lf, all, ok := openStore(*file, *keyFile, stderr)
	if !ok {
		return 1
	}
	cur, exists := all[account]
	if *slot == "next" && !exists {
		fmt.Fprintf(stderr, "hubctl: account %s has no active slot yet; set the active slot first\n", account)
		return 1
	}
	v := *version
	if v == 0 {
		v = 1
		if exists {
			v = cur.Active.Version + 1
			if cur.Next != nil && cur.Next.Version >= v {
				v = cur.Next.Version + 1
			}
		}
	}
	s := creds.Slot{Version: v, Values: values}
	if *slot == "active" {
		cur.Active = s
	} else {
		cur.Next = &s
	}
	all[account] = cur
	if err := lf.WriteAll(all); err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "set %s %s v%d keys=%s in %s\n", account, *slot, v, strings.Join(s.Keys(), ","), lf.Path())
	return 0
}

func credsList(args []string, stdout, stderr io.Writer) int {
	fs, file, keyFile := credsFlags("list", stderr)
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return 2
	}
	lf, err := creds.OpenLocalFile(*file, *keyFile)
	if err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	all, err := lf.ReadAll()
	if err != nil {
		fmt.Fprintf(stderr, "hubctl: %v\n", err)
		return 1
	}
	ids := make([]string, 0, len(all))
	for id := range all {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		a := all[id]
		fmt.Fprintf(stdout, "%s\tactive\tv%d\t%s\n", id, a.Active.Version, strings.Join(a.Active.Keys(), ","))
		if a.Next != nil {
			fmt.Fprintf(stdout, "%s\tnext\tv%d\t%s\n", id, a.Next.Version, strings.Join(a.Next.Keys(), ","))
		}
	}
	return 0
}

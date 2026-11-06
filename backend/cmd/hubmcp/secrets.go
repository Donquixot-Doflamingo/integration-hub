package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
)

// secret is a configuration value that must never be printed (HUB_API_KEY, HUB_ADMIN_TOKEN,
// HUBMCP_HTTP_TOKEN). fmt and slog render it as ***.
type secret string

func (secret) String() string       { return "***" }
func (secret) GoString() string     { return "***" }
func (secret) LogValue() slog.Value { return slog.StringValue("***") }

// minSecretLen is the shortest value redacted by value; shorter ones would mangle ordinary text.
const minSecretLen = 6

// secretSet redacts configured secrets by value, in logs and in tool output (contract rule 6).
type secretSet struct {
	plain [][]byte // raw values
	json  [][]byte // values as they appear inside a JSON string
}

func newSecretSet(values ...secret) *secretSet {
	s := &secretSet{}
	for _, v := range values {
		if len(v) < minSecretLen {
			continue
		}
		s.plain = append(s.plain, []byte(v))
		enc, _ := json.Marshal(string(v))
		s.json = append(s.json, enc[1:len(enc)-1])
	}
	return s
}

// redact replaces every secret in str with ***.
func (s *secretSet) redact(str string) string {
	for _, p := range s.plain {
		str = strings.ReplaceAll(str, string(p), "***")
	}
	return str
}

// redactJSON replaces every secret inside a JSON document with ***.
func (s *secretSet) redactJSON(b []byte) []byte {
	for _, p := range s.json {
		b = bytes.ReplaceAll(b, p, []byte("***"))
	}
	return b
}

// newLogger returns the JSON logger (stderr: stdout carries the MCP stdio stream). Every string
// attribute, including the message, is redacted by value.
func newLogger(w io.Writer, level slog.Level, secrets *secretSet) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Value.Kind() == slog.KindString {
				a.Value = slog.StringValue(secrets.redact(a.Value.String()))
			}
			return a
		},
	})
	return slog.New(h).With(slog.String("role", "hubmcp"), slog.String("version", version))
}

package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
)

// TestCredsWorkflow builds the dev file for both dev accounts the way a developer does, and
// checks that list never prints a value.
func TestCredsWorkflow(t *testing.T) {
	dir := t.TempDir()
	key, file := filepath.Join(dir, "dev.key"), filepath.Join(dir, "dev-secrets.enc")
	common := []string{"-file", file, "-key-file", key}
	steps := []struct {
		args []string
		code int
		out  string
	}{
		{[]string{"creds", "init-key", "-key-file", key}, 0, "wrote dev key"},
		{[]string{"creds", "init-key", "-key-file", key}, 1, ""},
		{append(append([]string{"creds", "set"}, common...), "ct-brandA-in", "account_id=TEST-ACCT-A-not-real", "passcode=test-passcode-A-not-real"), 0, "set ct-brandA-in active v1 keys=account_id,passcode"},
		{append(append([]string{"creds", "set"}, common...), "ct-brandB-us", "account_id=TEST-ACCT-B-not-real", "passcode=test-passcode-B-not-real"), 0, "v1"},
		{append(append([]string{"creds", "set", "-slot", "next"}, common...), "ct-brandB-us", "account_id=TEST-ACCT-B-not-real", "passcode=test-passcode-B2-not-real"), 0, "set ct-brandB-us next v2"},
		{append(append([]string{"creds", "set"}, common...), "ct-brandA-in", "passcode=short"), 2, ""},
		{append(append([]string{"creds", "set"}, common...), "ct-brandA-in", "novalue"), 2, ""},
		{append(append([]string{"creds", "set", "-slot", "next"}, common...), "ct-nobody", "token=abcdefgh"), 1, ""},
		{append([]string{"creds", "list"}, common...), 0, "ct-brandA-in\tactive\tv1\taccount_id,passcode\nct-brandB-us\tactive\tv1\taccount_id,passcode\nct-brandB-us\tnext\tv2\taccount_id,passcode\n"},
		{[]string{"creds", "bogus"}, 2, ""},
	}
	for i, s := range steps {
		var out, errOut bytes.Buffer
		code := run(s.args, &out, &errOut)
		if code != s.code || !strings.Contains(out.String(), s.out) {
			t.Fatalf("step %d %v: exit %d (want %d)\nstdout: %s\nstderr: %s", i, s.args[:2], code, s.code, out.String(), errOut.String())
		}
		if all := out.String() + errOut.String(); strings.Contains(all, "not-real") {
			t.Fatalf("step %d printed a secret value:\n%s", i, all)
		}
	}
	lf, err := creds.OpenLocalFile(file, key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := lf.Load(t.Context(), []string{"ct-brandA-in", "ct-brandB-us"})
	if err != nil || got["ct-brandA-in"].Active.Values["passcode"] != "test-passcode-A-not-real" || got["ct-brandB-us"].Next.Values["passcode"] != "test-passcode-B2-not-real" {
		t.Fatalf("file content = %+v, %v", got, err)
	}
}

func TestCredsListWrongKeyFailsClosed(t *testing.T) {
	dir := t.TempDir()
	key, other, file := filepath.Join(dir, "dev.key"), filepath.Join(dir, "other.key"), filepath.Join(dir, "s.enc")
	var out, errOut bytes.Buffer
	for _, args := range [][]string{
		{"creds", "init-key", "-key-file", key},
		{"creds", "init-key", "-key-file", other},
		{"creds", "set", "-file", file, "-key-file", key, "ct-brandA-in", "passcode=test-passcode-not-real"},
	} {
		if code := run(args, &out, &errOut); code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, errOut.String())
		}
	}
	errOut.Reset()
	if code := run([]string{"creds", "list", "-file", file, "-key-file", other}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "tampered") {
		t.Fatalf("list with the wrong key: exit %d, stderr %q", code, errOut.String())
	}
}

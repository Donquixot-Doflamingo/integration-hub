package creds

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Local dev credential source (DESIGN §9, TECH_SPEC §9): one AES-256-GCM encrypted JSON file
// holding {accountID: {active: {version, values}, next?: {...}}}, keyed by a 32-byte dev key
// read from HUB_DEV_KEY_FILE. Neither the key nor the encrypted file is ever committed.

const (
	// EnvDevKeyFile names the file holding the base64 dev key.
	EnvDevKeyFile = "HUB_DEV_KEY_FILE"
	// KeySize is the AES-256 key length in bytes.
	KeySize = 32

	fileFormat = "hub-dev-secrets/v1"
	fileAlg    = "AES-256-GCM"
)

// additionalData binds the ciphertext to this file format, so a blob sealed for another purpose
// with the same key does not open here.
var additionalData = []byte(fileFormat)

// envelope is the on-disk shape. []byte fields are base64 in JSON.
type envelope struct {
	Format     string `json:"format"`
	Alg        string `json:"alg"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

// GenerateKey returns a new random 32-byte key.
func GenerateKey() ([]byte, error) {
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		return nil, fmt.Errorf("creds: generate key: %w", err)
	}
	return k, nil
}

// EncodeKey is the key file content: standard base64 and a newline.
func EncodeKey(key []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(key) + "\n")
}

// DecodeKey parses key file content.
func DecodeKey(b []byte) ([]byte, error) {
	k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil {
		return nil, fmt.Errorf("creds: dev key is not base64: %w", err)
	}
	if len(k) != KeySize {
		return nil, fmt.Errorf("creds: dev key is %d bytes, want %d", len(k), KeySize)
	}
	return k, nil
}

// ReadKeyFile reads and decodes a key file. It refuses a file that group or others can read.
func ReadKeyFile(path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("creds: dev key file: %w", err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("creds: dev key file %s has mode %v; it must not be readable by group or others (chmod 600)", path, fi.Mode().Perm())
	}
	b, err := os.ReadFile(path) //nolint:gosec // path is operator configuration (HUB_DEV_KEY_FILE)
	if err != nil {
		return nil, fmt.Errorf("creds: dev key file: %w", err)
	}
	return DecodeKey(b)
}

// WriteKeyFile writes key to path with mode 0600. It refuses to overwrite an existing file
// unless overwrite is set.
func WriteKeyFile(path string, key []byte, overwrite bool) error {
	if len(key) != KeySize {
		return fmt.Errorf("creds: dev key is %d bytes, want %d", len(key), KeySize)
	}
	if !overwrite {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("creds: dev key file %s already exists (re-keying makes the encrypted file unreadable)", path)
		}
	}
	return writeFileAtomic(path, EncodeKey(key))
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("creds: dev key is %d bytes, want %d", len(key), KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("creds: cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creds: gcm: %w", err)
	}
	return aead, nil
}

// seal encrypts plaintext into the file envelope with a fresh random nonce.
func seal(key, plaintext []byte) ([]byte, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("creds: nonce: %w", err)
	}
	env := envelope{Format: fileFormat, Alg: fileAlg, Nonce: nonce, Ciphertext: aead.Seal(nil, nonce, plaintext, additionalData)}
	b, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("creds: encode envelope: %w", err)
	}
	return append(b, '\n'), nil
}

// open authenticates and decrypts an envelope. Any change to nonce, ciphertext or tag, a
// truncation or a wrong key fails with ErrDecrypt; nothing is returned in that case.
func open(key, data []byte) ([]byte, error) {
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("%w: not a dev secrets envelope: %w", ErrDecrypt, err)
	}
	if env.Format != fileFormat || env.Alg != fileAlg {
		return nil, fmt.Errorf("%w: unsupported format %q / alg %q (want %q / %q)", ErrDecrypt, env.Format, env.Alg, fileFormat, fileAlg)
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	if len(env.Nonce) != aead.NonceSize() {
		return nil, fmt.Errorf("%w: nonce is %d bytes, want %d", ErrDecrypt, len(env.Nonce), aead.NonceSize())
	}
	pt, err := aead.Open(nil, env.Nonce, env.Ciphertext, additionalData)
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

// LocalFile is the dev credential source. Load decrypts the whole file on each call, so it
// belongs off the request path: Cache.Warm and the background refresh call it, nothing else.
type LocalFile struct {
	path string
	key  []byte
}

// NewLocalFile returns a source for the encrypted file at path, sealed with key.
func NewLocalFile(path string, key []byte) (*LocalFile, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("creds: dev key is %d bytes, want %d", len(key), KeySize)
	}
	return &LocalFile{path: path, key: slices.Clone(key)}, nil
}

// OpenLocalFile reads the key from keyFile (usually $HUB_DEV_KEY_FILE) and returns the source.
func OpenLocalFile(path, keyFile string) (*LocalFile, error) {
	key, err := ReadKeyFile(keyFile)
	if err != nil {
		return nil, err
	}
	return NewLocalFile(path, key)
}

// Path is the encrypted file's path.
func (f *LocalFile) Path() string { return f.path }

// ReadAll decrypts the file. A missing file is fs.ErrNotExist (wrapped).
func (f *LocalFile) ReadAll() (map[string]AccountSecrets, error) {
	data, err := os.ReadFile(f.path)
	if err != nil {
		return nil, fmt.Errorf("creds: read %s: %w", f.path, err)
	}
	pt, err := open(f.key, data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", f.path, err)
	}
	defer clear(pt) // best effort: the decoded strings below are copies Go cannot zero
	var all map[string]AccountSecrets
	if err := json.Unmarshal(pt, &all); err != nil {
		return nil, fmt.Errorf("creds: %s: decrypted content is not valid JSON: %w", f.path, err)
	}
	if all == nil {
		all = map[string]AccountSecrets{}
	}
	return all, nil
}

// WriteAll validates, encrypts and atomically replaces the file (mode 0600).
func (f *LocalFile) WriteAll(all map[string]AccountSecrets) error {
	for _, id := range sortedIDs(all) {
		if err := all[id].Validate(); err != nil {
			return fmt.Errorf("creds: account %q: %w", id, err)
		}
	}
	pt, err := json.Marshal(all)
	if err != nil {
		return fmt.Errorf("creds: encode secrets: %w", err)
	}
	defer clear(pt)
	data, err := seal(f.key, pt)
	if err != nil {
		return err
	}
	return writeFileAtomic(f.path, data)
}

// Load decrypts the file and returns the requested accounts. Accounts absent from the file are
// reported in a wrapped ErrNotFound alongside the ones that were found; a file that fails
// authentication returns ErrDecrypt and nothing else.
func (f *LocalFile) Load(ctx context.Context, accountIDs []string) (map[string]AccountSecrets, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	all, err := f.ReadAll()
	if err != nil {
		return nil, err
	}
	out := make(map[string]AccountSecrets, len(accountIDs))
	var missing []string
	var errs []error
	for _, id := range accountIDs {
		s, ok := all[id]
		if !ok {
			missing = append(missing, id)
			continue
		}
		if err := s.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("creds: account %q in %s: %w", id, f.path, err))
			continue
		}
		out[id] = s
	}
	if len(missing) > 0 {
		errs = append(errs, fmt.Errorf("%w: %s not in %s", ErrNotFound, strings.Join(missing, ", "), f.path))
	}
	return out, errors.Join(errs...)
}

func sortedIDs(m map[string]AccountSecrets) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// writeFileAtomic writes data to a temp file in the same directory and renames it over path.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creds: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("creds: temp file: %w", err)
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(name)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("creds: chmod %s: %w", name, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("creds: write %s: %w", name, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("creds: sync %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("creds: close %s: %w", name, err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("creds: rename to %s: %w", path, err)
	}
	ok = true
	return nil
}

// IsNotExist reports whether err means the encrypted file does not exist yet.
func IsNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }

// Package vault encrypts secrets at rest (passwords, API tokens, SSH keys, TOTP
// secrets) with AES-256-GCM under one master key.
//
// The key comes from HOMEWARDEN_MASTER_KEY (base64, 32 bytes) when set, otherwise from
// master.key in the data folder, which is created on first start. Losing the key makes
// every stored secret unreadable, so it is part of what backing up appdata means.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const prefix = "v1:"

// Vault seals and opens secrets.
type Vault struct {
	aead cipher.AEAD
}

// Load returns a vault using the key from the environment or dataDir/master.key,
// creating that file if neither exists.
func Load(dataDir string) (*Vault, error) {
	key, err := loadKey(dataDir)
	if err != nil {
		return nil, err
	}
	return New(key)
}

// New returns a vault for a 32-byte key.
func New(key []byte) (*Vault, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("master key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Vault{aead: aead}, nil
}

func loadKey(dataDir string) ([]byte, error) {
	if env := strings.TrimSpace(os.Getenv("HOMEWARDEN_MASTER_KEY")); env != "" {
		key, err := base64.StdEncoding.DecodeString(env)
		if err != nil {
			return nil, fmt.Errorf("HOMEWARDEN_MASTER_KEY is not valid base64: %w", err)
		}
		return key, nil
	}
	path := filepath.Join(dataDir, "master.key")
	raw, err := os.ReadFile(path)
	if err == nil {
		return base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	return key, nil
}

// Encrypt seals plain text into a self-describing string safe to store in the database.
func (v *Vault) Encrypt(plain []byte) (string, error) {
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := v.aead.Seal(nonce, nonce, plain, nil)
	return prefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// EncryptString is Encrypt for strings.
func (v *Vault) EncryptString(s string) (string, error) { return v.Encrypt([]byte(s)) }

// Decrypt opens a string produced by Encrypt.
func (v *Vault) Decrypt(s string) ([]byte, error) {
	if !strings.HasPrefix(s, prefix) {
		return nil, errors.New("vault: unknown secret format")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, prefix))
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	n := v.aead.NonceSize()
	if len(raw) < n {
		return nil, errors.New("vault: secret too short")
	}
	plain, err := v.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return nil, errors.New("vault: secret cannot be decrypted with this master key")
	}
	return plain, nil
}

// DecryptString is Decrypt for strings.
func (v *Vault) DecryptString(s string) (string, error) {
	b, err := v.Decrypt(s)
	return string(b), err
}

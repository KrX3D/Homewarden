package store

import (
	"context"
	"database/sql"
	"errors"
)

// Credential types.
const (
	CredSSHKey   = "ssh_key"  // private key (secret) + authorized_keys line (public)
	CredPassword = "password" // username + password
	CredToken    = "token"    // API key or token
)

// Credential is a stored secret. Secret holds the vault-encrypted value and is never
// serialised.
type Credential struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Username  string `json:"username"`
	Secret    string `json:"-"`
	Public    string `json:"public"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
	UsedBy    int    `json:"usedBy"`
}

const credCols = "id, name, type, username, secret, public, created_at, updated_at"

func scanCred(row interface{ Scan(...any) error }) (Credential, error) {
	var c Credential
	err := row.Scan(&c.ID, &c.Name, &c.Type, &c.Username, &c.Secret, &c.Public, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

// Credentials lists all credentials with how many devices use each.
func (s *Store) Credentials(ctx context.Context) ([]Credential, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+credCols+" FROM credentials ORDER BY name COLLATE NOCASE")
	if err != nil {
		return nil, err
	}
	out := []Credential{}
	for rows.Next() {
		c, err := scanCred(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, c)
	}
	rows.Close()
	for i := range out {
		out[i].UsedBy, _ = s.CredentialUsage(ctx, out[i].ID)
	}
	return out, nil
}

// Credential returns one credential including its encrypted secret.
func (s *Store) Credential(ctx context.Context, id int64) (Credential, error) {
	c, err := scanCred(s.DB.QueryRowContext(ctx, "SELECT "+credCols+" FROM credentials WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// CreateCredential stores a credential (secret already encrypted).
func (s *Store) CreateCredential(ctx context.Context, c Credential) (Credential, error) {
	t := now()
	res, err := s.DB.ExecContext(ctx, "INSERT INTO credentials(name, type, username, secret, public, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?)",
		c.Name, c.Type, c.Username, c.Secret, c.Public, t, t)
	if err != nil {
		return Credential{}, err
	}
	id, _ := res.LastInsertId()
	return s.Credential(ctx, id)
}

// UpdateCredential stores name, username and (when non-empty) a new secret/public.
func (s *Store) UpdateCredential(ctx context.Context, c Credential) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE credentials SET name = ?, username = ?, secret = ?, public = ?, updated_at = ? WHERE id = ?",
		c.Name, c.Username, c.Secret, c.Public, now(), c.ID)
	return err
}

// DeleteCredential removes a credential.
func (s *Store) DeleteCredential(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM credentials WHERE id = ?", id)
	return err
}

// CredentialUsage counts devices whose config references the credential in any
// field named ...credentialId.
func (s *Store) CredentialUsage(ctx context.Context, id int64) (int, error) {
	// A reference is a ...credentialId field, or an integration setting of type
	// credential (apiToken, apiKey, ...) inside "settings".
	match := `(key LIKE '%credentialId'`
	args := []any{}
	if s.CredentialKeys != nil {
		for _, k := range s.CredentialKeys() {
			match += ` OR (key = ? AND path LIKE '%.settings')`
			args = append(args, k)
		}
	}
	match += `) AND type IN ('integer', 'real', 'text') AND CAST(value AS INTEGER) = ?`
	args = append(args, id)
	var n int
	err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM devices d WHERE EXISTS (SELECT 1 FROM json_tree(d.config) WHERE `+match+`)`, args...).Scan(&n)
	if err != nil {
		return n, err
	}
	// Integration setups count as one user each, monitors too.
	var m, k int
	err = s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM settings st WHERE st.key LIKE 'integration.defaults.%' AND json_valid(st.value) AND EXISTS (
			SELECT 1 FROM json_tree(st.value) WHERE `+match+`)`, args...).Scan(&m)
	if err != nil {
		return n, err
	}
	err = s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM monitors mo WHERE json_valid(mo.config) AND EXISTS (SELECT 1 FROM json_tree(mo.config) WHERE `+match+`)`, args...).Scan(&k)
	return n + m + k, err
}

// KnownHost is a pinned SSH host key.
type KnownHost struct {
	Address     string `json:"address"`
	KeyType     string `json:"keyType"`
	Fingerprint string `json:"fingerprint"`
	Key         string `json:"-"`
	FirstSeen   int64  `json:"firstSeen"`
}

// KnownHost returns the pinned key for host:port.
func (s *Store) KnownHost(ctx context.Context, addr string) (KnownHost, error) {
	var k KnownHost
	err := s.DB.QueryRowContext(ctx, "SELECT address, key_type, fingerprint, key, first_seen FROM known_hosts WHERE address = ?", addr).
		Scan(&k.Address, &k.KeyType, &k.Fingerprint, &k.Key, &k.FirstSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return k, ErrNotFound
	}
	return k, err
}

// PinHost stores (or replaces) the key for host:port.
func (s *Store) PinHost(ctx context.Context, k KnownHost) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO known_hosts(address, key_type, fingerprint, key, first_seen) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(address) DO UPDATE SET key_type = excluded.key_type, fingerprint = excluded.fingerprint, key = excluded.key, first_seen = excluded.first_seen`,
		k.Address, k.KeyType, k.Fingerprint, k.Key, now())
	return err
}

// ForgetHost removes a pinned key so the next connection pins a new one.
func (s *Store) ForgetHost(ctx context.Context, addr string) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM known_hosts WHERE address = ?", addr)
	return err
}

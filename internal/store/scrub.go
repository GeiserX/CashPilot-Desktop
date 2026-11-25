package store

import (
	"context"
	"database/sql"
	"encoding/json"
)

// ScrubCredential deletes key from slug's stored credentials and leaves no older
// copy of it in the database file. The blob is encrypted, but its key stays in the
// OS keychain, so a copy left in a free page or the log could still be decrypted.
// And every earlier save of the form left such a copy behind, before secure_delete
// was on. So the delete runs with secure_delete on, VACUUM then rebuilds the file
// without any free space, and the log is truncated. removed is false when key was
// not stored. busy is SQLite's answer that a reader kept the log from being
// emptied: the key is deleted, and an old copy may stay in the log until the next
// checkpoint.
func (s *Store) ScrubCredential(slug, key string) (removed, busy bool, err error) {
	err = s.withSecureDelete(func(c *sql.Conn) error {
		busy = false
		ctx := context.Background()
		// A retry after a busy VACUUM finds the key already gone: removed stays
		// true from the try that deleted it, and the scrub runs again.
		if !removed {
			done, err := s.dropCredentialKey(ctx, c, slug, key)
			if err != nil || !done {
				return err
			}
			removed = true
		}
		if _, err := c.ExecContext(ctx, `VACUUM`); err != nil {
			return err
		}
		var cerr error
		busy, cerr = checkpoint(ctx, c)
		return cerr
	})
	return removed, busy, err
}

// dropCredentialKey rewrites slug's stored credentials without key. done is false
// when key was not stored.
func (s *Store) dropCredentialKey(ctx context.Context, c *sql.Conn, slug, key string) (bool, error) {
	var encrypted string
	switch err := c.QueryRowContext(ctx, `SELECT value FROM credentials WHERE slug = ?`, slug).Scan(&encrypted); {
	case err == sql.ErrNoRows:
		return false, nil
	case err != nil:
		return false, err
	}
	raw, err := s.decrypt(encrypted)
	if err != nil {
		return false, err
	}
	var values map[string]string
	if err := json.Unmarshal(raw, &values); err != nil {
		return false, err
	}
	if _, ok := values[key]; !ok {
		return false, nil
	}
	delete(values, key)
	if raw, err = json.Marshal(values); err != nil {
		return false, err
	}
	sealed, err := s.encrypt(raw)
	if err != nil {
		return false, err
	}
	if _, err := c.ExecContext(ctx, `UPDATE credentials SET value = ?, updated_at = datetime('now') WHERE slug = ?`, sealed, slug); err != nil {
		return false, err
	}
	return true, nil
}

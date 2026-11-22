package core

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// Agent keys: nobody acts on behalf of another agent.
//
// Registering gives a session its agent's key (Agent.Key), and every call
// that acts as an agent must carry it (the hub checks it, see Authorize).
// The key is HMAC-SHA256(hub secret, agent id): every process of the same
// session (its MCP server, each short-lived hook) gets the same key from its
// own registration, so nothing has to be handed from one process to another,
// while the database keeps only the key's SHA-256 (agents.key_hash). The hub
// secret is 32 random bytes in hub.key next to the database (0600).
//
// Honest limit: a process of the same OS user can read hub.key or the
// database and act as anyone. This stops accidental and cross-session
// impersonation (an agent passing another's id, a confused session, a bug),
// not local malware.
//
// No fallback: an agent row without key_hash (written by an older version)
// can't be acted as until a session registers it with this version, which
// issues the key. That costs live sessions nothing: every process of this
// version registers through its own hub client before acting (the MCP
// server at start, each hook per call), and older binaries still running
// don't check keys at all. Only a process acting as an agent it never
// registered is refused, which is the point.

// ErrNotYours refuses a call that acts as an agent without that agent's key.
var ErrNotYours = errors.New("refused: only an agent's own session can act as it")

const secretLen = 32

// hubSecret reads the hub secret, creating it on first use.
func (s *Store) hubSecret() ([]byte, error) {
	s.keyMu.Lock()
	defer s.keyMu.Unlock()
	if s.secret != nil {
		return s.secret, nil
	}
	b, err := readOrMakeSecret(s.keyPath)
	if err != nil {
		return nil, fmt.Errorf("agent keys: %w", err)
	}
	s.secret = b
	return b, nil
}

func readOrMakeSecret(path string) ([]byte, error) {
	for range 50 {
		b, err := os.ReadFile(path)
		switch {
		case err == nil && len(b) >= secretLen:
			return b[:secretLen], nil
		case errors.Is(err, fs.ErrNotExist):
			if b, err := makeSecret(path); err == nil || !errors.Is(err, fs.ErrExist) {
				return b, err
			}
		case err != nil:
			return nil, err
		}
		time.Sleep(2 * time.Millisecond) // another process is writing it
	}
	return nil, fmt.Errorf("%s is not a valid key file; delete it and every session gets a new key", path)
}

// makeSecret creates the secret file only if nobody else has (fs.ErrExist).
func makeSecret(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	b := make([]byte, secretLen)
	_, err = rand.Read(b)
	if err == nil {
		_, err = f.Write(b)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	return b, nil
}

func agentKey(secret []byte, id string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("interlinked agent key\x00" + id))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func hashKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

// issueKey records the hash of agent id's key (inside Register's tx) and
// returns the key.
func issueKey(tx *sql.Tx, secret []byte, id string) (string, error) {
	key := agentKey(secret, id)
	h := hashKey(key)
	_, err := tx.Exec(`UPDATE agents SET key_hash=? WHERE id=? AND key_hash<>?`, h, id, h)
	return key, err
}

// Authorize checks that a call acting as agent id carries its key. An
// unknown or empty id passes: the method itself says the agent is unknown.
func (s *Store) Authorize(ctx context.Context, id, key string) error {
	if id == "" {
		return nil
	}
	var stored, name string
	err := s.db.QueryRowContext(ctx, `SELECT key_hash, name FROM agents WHERE id=?`, id).Scan(&stored, &name)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	case stored == "":
		return fmt.Errorf("%w: %q was registered by an older version and has no key yet; only its own session, registering again, gets one", ErrNotYours, name)
	}
	if key != "" && subtle.ConstantTimeCompare([]byte(hashKey(key)), []byte(stored)) == 1 {
		return nil
	}
	return fmt.Errorf("%w: this call acts as %q without its key; act as yourself (whoami), or ask %s with send_message", ErrNotYours, name, name)
}

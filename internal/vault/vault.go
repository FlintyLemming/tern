// Package vault implements envelope encryption for credentials.
//
// Every credential is sealed with a random 256-bit data key (XChaCha20-Poly1305).
// The data key itself is stored in the database, wrapped by a master key that
// comes either from a key file or from a passphrase run through Argon2id.
package vault

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"

	"tern/internal/store"
)

type Mode string

const (
	ModeKeyfile    Mode = "keyfile"
	ModePassphrase Mode = "passphrase"
)

type State string

const (
	StateUninitialized   State = "uninitialized"
	StateUnlocked        State = "unlocked"
	StateLocked          State = "locked"
	StateKeyfileMissing  State = "keyfile_missing"
	StateKeyfileMismatch State = "keyfile_mismatch"
)

var (
	ErrLocked             = errors.New("vault: locked")
	ErrWrongPassphrase    = errors.New("vault: wrong passphrase")
	ErrAlreadyInitialized = errors.New("vault: already initialized")
	// ErrKeyfileNotRemoved is returned by SwitchMode when the switch to
	// passphrase mode succeeded but the old key file is still on disk.
	ErrKeyfileNotRemoved = errors.New("vault: switched to passphrase mode, but the old key file could not be removed")
)

const (
	metaMode = "vault.mode"
	metaDEK  = "vault.dek"
	metaKDF  = "vault.kdf"

	dekAAD       = "tern/dek/v1"
	sealedPrefix = "v1."
	keyLen       = 32
)

// MetaStore is the part of store.Store the vault needs.
type MetaStore interface {
	GetMeta(ctx context.Context, key string) (value string, ok bool, err error)
	SetMeta(ctx context.Context, key, value string) error
	Tx(ctx context.Context, fn func(*sql.Tx) error) error
}

// Vault holds the data key in memory while unlocked.
type Vault struct {
	st      MetaStore
	keyPath string

	mu    sync.RWMutex
	state State
	mode  Mode
	dek   []byte
}

func New(st MetaStore, secretsDir string) *Vault {
	return &Vault{st: st, keyPath: filepath.Join(secretsDir, "master.key"), state: StateUninitialized}
}

func (v *Vault) State() State {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.state
}

func (v *Vault) Mode() Mode {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.mode
}

// Load reads the stored mode at startup. Key-file mode unlocks here; passphrase
// mode stays locked until Unlock. Load never creates or modifies any file.
func (v *Vault) Load(ctx context.Context) (State, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	mode, ok, err := v.st.GetMeta(ctx, metaMode)
	if err != nil {
		return v.state, err
	}
	if !ok {
		v.set(StateUninitialized, "", nil)
		return v.state, nil
	}
	switch Mode(mode) {
	case ModePassphrase:
		v.set(StateLocked, ModePassphrase, nil)
	case ModeKeyfile:
		master, kst := readKeyfile(v.keyPath)
		if kst != keyfileOK {
			st := StateKeyfileMismatch
			if kst == keyfileAbsent {
				st = StateKeyfileMissing
			}
			v.set(st, ModeKeyfile, nil)
			break
		}
		dek, err := v.unwrapDEK(ctx, master)
		if err != nil {
			if errors.Is(err, errUnwrap) {
				v.set(StateKeyfileMismatch, ModeKeyfile, nil)
				break
			}
			return v.state, err
		}
		v.set(StateUnlocked, ModeKeyfile, dek)
	default:
		return v.state, fmt.Errorf("vault: unknown mode %q in database", mode)
	}
	return v.state, nil
}

// Init generates the data key and wraps it for mode. It succeeds once per database.
func (v *Vault) Init(ctx context.Context, mode Mode, passphrase string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if _, ok, err := v.st.GetMeta(ctx, metaMode); err != nil {
		return err
	} else if ok {
		return ErrAlreadyInitialized
	}
	dek := make([]byte, keyLen)
	rand.Read(dek)
	if err := v.wrapAndStore(ctx, mode, passphrase, dek); err != nil {
		return err
	}
	v.set(StateUnlocked, mode, dek)
	return nil
}

// SwitchMode re-wraps the data key for another mode (or a new passphrase).
// Sealed credentials are untouched. Leaving key-file mode deletes the key
// file; if that fails the switch still stands and ErrKeyfileNotRemoved is
// returned.
func (v *Vault) SwitchMode(ctx context.Context, mode Mode, passphrase string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.state != StateUnlocked {
		return ErrLocked
	}
	if err := v.wrapAndStore(ctx, mode, passphrase, v.dek); err != nil {
		return err
	}
	prev := v.mode
	v.mode = mode
	if prev == ModeKeyfile && mode == ModePassphrase {
		// The data key does not rotate, so the old key file together with any
		// old copy of the database would still open every credential.
		if err := os.Remove(v.keyPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: delete %s by hand: %v", ErrKeyfileNotRemoved, v.keyPath, err)
		}
	}
	return nil
}

// wrapAndStore obtains the master key for mode, wraps dek with it and writes
// vault.mode, vault.dek and vault.kdf in one transaction.
func (v *Vault) wrapAndStore(ctx context.Context, mode Mode, passphrase string, dek []byte) error {
	var master []byte
	var kdfJSON string
	var err error
	switch mode {
	case ModeKeyfile:
		master, err = ensureKeyfile(v.keyPath)
	case ModePassphrase:
		master, kdfJSON, err = newPassphraseKey(passphrase)
	default:
		err = fmt.Errorf("vault: unknown mode %q", mode)
	}
	if err != nil {
		return err
	}
	wrapped, err := seal(master, dekAAD, dek)
	if err != nil {
		return err
	}
	return v.st.Tx(ctx, func(tx *sql.Tx) error {
		if err := store.SetMetaTx(ctx, tx, metaMode, string(mode)); err != nil {
			return err
		}
		if err := store.SetMetaTx(ctx, tx, metaDEK, wrapped); err != nil {
			return err
		}
		if kdfJSON == "" {
			return store.DeleteMetaTx(ctx, tx, metaKDF)
		}
		return store.SetMetaTx(ctx, tx, metaKDF, kdfJSON)
	})
}

// errUnwrap means the master key does not open vault.dek.
var errUnwrap = errors.New("vault: master key does not match the stored data key")

func (v *Vault) unwrapDEK(ctx context.Context, master []byte) ([]byte, error) {
	wrapped, ok, err := v.st.GetMeta(ctx, metaDEK)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("vault: database has vault.mode but no vault.dek")
	}
	dek, err := open(master, dekAAD, wrapped)
	if err != nil || len(dek) != keyLen {
		return nil, errUnwrap
	}
	return dek, nil
}

func (v *Vault) set(st State, mode Mode, dek []byte) {
	v.state, v.mode, v.dek = st, mode, dek
}

// Seal encrypts plaintext bound to aad ("<table>/<record id>/<field>") and
// returns "v1." + base64url(24-byte nonce ‖ ciphertext).
func (v *Vault) Seal(aad string, plaintext []byte) (string, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.state != StateUnlocked {
		return "", ErrLocked
	}
	return seal(v.dek, aad, plaintext)
}

// Open reverses Seal. It fails when aad differs from the one used to seal.
func (v *Vault) Open(aad string, sealed string) ([]byte, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.state != StateUnlocked {
		return nil, ErrLocked
	}
	return open(v.dek, aad, sealed)
}

func seal(key []byte, aad string, plaintext []byte) (string, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, chacha20poly1305.NonceSizeX, chacha20poly1305.NonceSizeX+len(plaintext)+aead.Overhead())
	rand.Read(nonce)
	return sealedPrefix + base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, plaintext, []byte(aad))), nil
}

var errMalformed = errors.New("vault: malformed ciphertext")

func open(key []byte, aad string, sealed string) ([]byte, error) {
	body, ok := strings.CutPrefix(sealed, sealedPrefix)
	if !ok {
		return nil, errMalformed
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(body)
	if err != nil || len(raw) < chacha20poly1305.NonceSizeX+chacha20poly1305.Overhead {
		return nil, errMalformed
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	nonce, ct := raw[:chacha20poly1305.NonceSizeX], raw[chacha20poly1305.NonceSizeX:]
	plain, err := aead.Open(nil, nonce, ct, []byte(aad))
	if err != nil {
		return nil, errors.New("vault: ciphertext does not authenticate")
	}
	return plain, nil
}

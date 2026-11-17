package vault

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"golang.org/x/crypto/argon2"
)

// maxPassphraseLen mirrors auth.MaxSecretLen: longer input never reaches Argon2.
const maxPassphraseLen = 1024

type kdfParams struct {
	Salt      string `json:"salt"` // base64, 16 bytes
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memory_kib"`
	Threads   uint8  `json:"threads"`
}

func (k kdfParams) derive(passphrase string) ([]byte, error) {
	salt, err := base64.StdEncoding.DecodeString(k.Salt)
	if err != nil || len(salt) == 0 || k.Time == 0 || k.Threads == 0 || k.MemoryKiB < 8*uint32(k.Threads) {
		return nil, errors.New("vault: stored vault.kdf is invalid")
	}
	return argon2.IDKey([]byte(passphrase), salt, k.Time, k.MemoryKiB, k.Threads, keyLen), nil
}

// Unlock opens a vault that tern 0.1 left in passphrase mode: it derives
// the master key from passphrase and loads the data key. It is a no-op on a
// vault that is already unlocked.
func (v *Vault) Unlock(ctx context.Context, passphrase string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.state == StateUnlocked {
		return nil
	}
	if v.state != StateLocked {
		return fmt.Errorf("vault: cannot unlock with a passphrase in state %q", v.state)
	}
	if len(passphrase) > maxPassphraseLen {
		return ErrWrongPassphrase
	}
	raw, ok, err := v.st.GetMeta(ctx, metaKDF)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("vault: passphrase mode but vault.kdf is missing")
	}
	var k kdfParams
	if err := json.Unmarshal([]byte(raw), &k); err != nil {
		return errors.New("vault: stored vault.kdf is invalid")
	}
	master, err := k.derive(passphrase)
	if err != nil {
		return err
	}
	dek, err := v.unwrapDEK(ctx, master)
	if errors.Is(err, errUnwrap) {
		return ErrWrongPassphrase
	}
	if err != nil {
		return err
	}
	v.set(StateUnlocked, ModePassphrase, dek)
	return nil
}

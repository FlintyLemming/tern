// Package access turns a stored connection into what a child process needs
// to use it: for SSH an agent socket and a generated ssh_config, for WebDAV
// the decrypted credentials. This is the only place secrets are unsealed for
// transfer tools.
package access

import (
	"context"
	"fmt"

	"tern/internal/model"
	"tern/internal/redact"
	"tern/internal/rundir"
	"tern/internal/sshx"
	"tern/internal/store"
	"tern/internal/vault"
	"tern/internal/webdavx"
)

// Access is the temporary material for one connection during one run.
type Access struct {
	Kind string // model.ConnSSH | model.ConnWebDAV

	// SSH. Usage: ssh -F <SSHConfig> tern-target
	SSHConfig string
	Target    sshx.Target

	// WebDAV. Pass must reach the tool through its environment or stdin,
	// never its command line.
	URL, User, Pass string
}

// Opener unseals connections.
type Opener struct {
	Store *store.Store
	Vault *vault.Vault
}

// sshMaterial loads and decrypts the key of an SSH connection.
func (o Opener) sshMaterial(ctx context.Context, c model.Connection) (sshx.Target, sshx.Parsed, []byte, error) {
	t := sshx.Target{Host: c.Host, Port: c.Port, User: c.Username, HostKey: c.HostKey}
	privSealed, passSealed, err := o.Store.KeySecrets(ctx, c.SSHKeyID)
	if err != nil {
		return t, sshx.Parsed{}, nil, fmt.Errorf("access: key of connection %s: %w", c.ID, err)
	}
	privPEM, err := o.Vault.Open("ssh_keys/"+c.SSHKeyID+"/private", privSealed)
	if err != nil {
		return t, sshx.Parsed{}, nil, fmt.Errorf("access: private key %s: %w", c.SSHKeyID, err)
	}
	defer clear(privPEM)
	var passphrase []byte
	if passSealed != "" {
		if passphrase, err = o.Vault.Open("ssh_keys/"+c.SSHKeyID+"/passphrase", passSealed); err != nil {
			return t, sshx.Parsed{}, nil, fmt.Errorf("access: passphrase of key %s: %w", c.SSHKeyID, err)
		}
	}
	p, err := sshx.ParsePrivate(privPEM, passphrase)
	if err != nil {
		return t, sshx.Parsed{}, nil, fmt.Errorf("access: stored key %s: %w", c.SSHKeyID, err)
	}
	return t, p, passphrase, nil
}

func (o Opener) davPassword(ctx context.Context, c model.Connection) (string, error) {
	sealed, err := o.Store.ConnectionPassword(ctx, c.ID)
	if err != nil {
		return "", err
	}
	if sealed == "" {
		return "", nil
	}
	plain, err := o.Vault.Open("connections/"+c.ID+"/password", sealed)
	if err != nil {
		return "", fmt.Errorf("access: password of connection %s: %w", c.ID, err)
	}
	return string(plain), nil
}

func (o Opener) load(ctx context.Context, connID string) (model.Connection, error) {
	if o.Vault.State() != vault.StateUnlocked {
		return model.Connection{}, vault.ErrLocked
	}
	return o.Store.GetConnection(ctx, connID)
}

// Open prepares connID for use by child processes of one run. Files go into
// dir; secrets that could show up in tool output are registered with red.
// The returned function releases everything and is safe to call twice. With
// a locked vault the error is vault.ErrLocked.
func (o Opener) Open(ctx context.Context, connID string, dir *rundir.Dir, red *redact.Redactor) (Access, func(), error) {
	c, err := o.load(ctx, connID)
	if err != nil {
		return Access{}, nil, err
	}
	switch c.Type {
	case model.ConnSSH:
		t, p, passphrase, err := o.sshMaterial(ctx, c)
		if err != nil {
			return Access{}, nil, err
		}
		if red != nil {
			red.Add(passphrase)
		}
		ag, err := sshx.StartAgent(dir, p.Raw)
		if err != nil {
			return Access{}, nil, err
		}
		cfg, err := sshx.WriteConfig(dir, t, ag.Socket())
		if err != nil {
			ag.Close()
			return Access{}, nil, err
		}
		return Access{Kind: c.Type, SSHConfig: cfg, Target: t}, func() { ag.Close() }, nil
	case model.ConnWebDAV:
		pass, err := o.davPassword(ctx, c)
		if err != nil {
			return Access{}, nil, err
		}
		if red != nil {
			red.Add([]byte(pass))
		}
		return Access{Kind: c.Type, URL: c.URL, User: c.Username, Pass: pass}, func() {}, nil
	}
	return Access{}, nil, fmt.Errorf("access: connection %s has unknown type %q", c.ID, c.Type)
}

// Check runs the preflight for connID. A failed preflight is a
// *model.KindError; anything else (unknown connection, locked vault, broken
// stored data) is an ordinary error.
func (o Opener) Check(ctx context.Context, connID string) error {
	c, err := o.load(ctx, connID)
	if err != nil {
		return err
	}
	switch c.Type {
	case model.ConnSSH:
		t, p, _, err := o.sshMaterial(ctx, c)
		if err != nil {
			return err
		}
		return sshx.Check(ctx, t, p.Signer)
	case model.ConnWebDAV:
		pass, err := o.davPassword(ctx, c)
		if err != nil {
			return err
		}
		return webdavx.Check(ctx, c.URL, c.Username, pass)
	}
	return fmt.Errorf("access: connection %s has unknown type %q", c.ID, c.Type)
}

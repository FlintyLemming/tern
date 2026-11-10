package sshx

import (
	"fmt"
	"strings"

	"tern/internal/model"
	"tern/internal/rundir"
)

// Alias is the only host name child processes ever use: `ssh -F <config>
// tern-target`. The real address lives in the generated config.
const Alias = "tern-target"

// The three engines share this file. It must stay byte-for-byte what the
// plan specifies; config_test.go holds the golden copy.
const configTemplate = `Host tern-target
  HostName %s
  Port %d
  User %s
  HostKeyAlias tern-target
  UserKnownHostsFile %s/known_hosts
  GlobalKnownHostsFile /dev/null
  StrictHostKeyChecking yes
  HostKeyAlgorithms %s
  IdentityAgent %s
  IdentityFile none
  PubkeyAuthentication yes
  PasswordAuthentication no
  KbdInteractiveAuthentication no
  BatchMode yes
  ServerAliveInterval 15
  ServerAliveCountMax 4
  ConnectTimeout 15
  ControlMaster no
  ForwardAgent no
  LogLevel ERROR
`

// plainPath refuses paths ssh_config would not take literally: whitespace
// splits arguments, '%' starts a token, quotes and '#' have meaning too.
func plainPath(p string) error {
	if p == "" || strings.ContainsAny(p, " \t\r\n%\"'#") {
		return fmt.Errorf("sshx: path %q cannot be written to ssh_config; use a runtime directory without spaces or special characters", p)
	}
	return nil
}

// WriteConfig writes <dir>/known_hosts (the one confirmed host key) and
// <dir>/ssh_config, and returns the latter's path. Every value is validated
// again here, whatever the caller checked before.
func WriteConfig(dir *rundir.Dir, t Target, agentSock string) (path string, err error) {
	if c := model.CheckHost(t.Host); c != "" {
		return "", fmt.Errorf("sshx: host %q is %s", t.Host, c)
	}
	if c := model.CheckPort(t.Port); c != "" {
		return "", fmt.Errorf("sshx: port %d is %s", t.Port, c)
	}
	if c := model.CheckSSHUser(t.User); c != "" {
		return "", fmt.Errorf("sshx: user name %q is %s", t.User, c)
	}
	hostKey, err := ParseAuthorized(t.HostKey)
	if err != nil {
		return "", fmt.Errorf("sshx: stored host key: %w", err)
	}
	if err := plainPath(dir.Path); err != nil {
		return "", err
	}
	if err := plainPath(agentSock); err != nil {
		return "", err
	}

	if _, err := dir.WriteSecret("known_hosts", []byte(Alias+" "+AuthorizedKey(hostKey)+"\n")); err != nil {
		return "", err
	}
	cfg := fmt.Sprintf(configTemplate, t.Host, t.Port, t.User, dir.Path,
		strings.Join(HostKeyAlgos(hostKey.Type()), ","), agentSock)
	return dir.WriteSecret("ssh_config", []byte(cfg))
}

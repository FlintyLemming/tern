package sysinfo

import "tern/internal/config"

// Mounts tells whether the data and secrets directories are on volumes
// mounted into the container. What tern writes anywhere else is lost when
// the container is recreated.
type Mounts struct {
	Data    bool `json:"data"`
	Secrets bool `json:"secrets"`
}

// CheckMounts asks isMounted (mounts.IsMounted in production) about both
// directories. A check that fails counts as not mounted. With
// cfg.AllowUnmounted both count as mounted.
func CheckMounts(cfg config.Config, isMounted func(string) (bool, error)) Mounts {
	if cfg.AllowUnmounted {
		return Mounts{Data: true, Secrets: true}
	}
	on := func(dir string) bool {
		ok, err := isMounted(dir)
		return err == nil && ok
	}
	return Mounts{Data: on(cfg.DataDir), Secrets: on(cfg.SecretsDir)}
}

package docker

import (
	"github.com/rtxnik/workspace-cli/internal/config"
)

// backupName is the deterministic name the recreate transaction renames the
// previous-good proxy container to, so an interrupted run is recoverable on the
// next invocation (spec §2.2).
func backupName(cfg config.Config) string {
	return cfg.ProxyContainer + "-backup"
}

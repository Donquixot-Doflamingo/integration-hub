package roles

import (
	"context"
	"log/slog"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/audit"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
)

// auditSealer is the sealed-policy key source (HUB-208, DESIGN §13.2): in phase 1 the
// per-tenant data keys derive from the localfile dev key (audit.LocalKeys, HKDF with its own
// domain label). Without the key file, sealed integrations keep hashes and sizes only and
// unseal answers 500: never clear bytes. The key itself is never logged.
func auditSealer(ctx context.Context, cfg Config, log *slog.Logger) *audit.Sealer {
	master, err := creds.ReadKeyFile(cfg.DevKeyFile)
	if err == nil {
		var keys *audit.LocalKeys
		if keys, err = audit.NewLocalKeys(master); err == nil {
			clear(master)
			return audit.NewSealer(keys)
		}
	}
	log.WarnContext(ctx, "no seal key: sealed payloads are stored as hashes and sizes only (hubctl creds init-key)",
		slog.String("key_file", cfg.DevKeyFile), slog.String("error", err.Error()))
	return nil
}

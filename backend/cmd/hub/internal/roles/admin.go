package roles

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/control/adminapi"
	"github.com/Donquixot-Doflamingo/integration-hub/backend/internal/creds"
)

// adminHandler is the admin role's public surface: /admin/v1 and, in dev, the OIDC issuer stub
// (/.well-known/openid-configuration, /oidc/*).
func adminHandler(ctx context.Context, cfg Config, log *slog.Logger, st *stores, reg *roleRegistry) (http.Handler, error) {
	var verifier adminapi.Verifier
	var dev *adminapi.DevIssuer
	switch {
	case cfg.AdminDevOIDC:
		var err error
		if dev, err = adminapi.NewDevIssuer(cfg.AdminPublicURL); err != nil {
			return nil, err
		}
		verifier = dev.Verifier()
		log.WarnContext(ctx, "admin dev OIDC stub is on: it mints tokens for any caller (dev only)", slog.String("issuer", dev.Issuer))
	case cfg.OIDCIssuer != "":
		verifier = &adminapi.JWTVerifier{Issuer: cfg.OIDCIssuer, Audience: cfg.OIDCAudience, Keys: adminapi.RemoteKeys(cfg.OIDCIssuer, nil)}
	default:
		log.WarnContext(ctx, "no OIDC issuer configured: every /admin/v1 request answers 401 (set HUB_OIDC_ISSUER or HUB_ADMIN_DEV_OIDC=true)")
	}
	pub := &control.Publisher{
		Pool: st.pg, Archive: reg.archive, Bootstrap: reg.src, Guard: control.RetireGuard(st.pg),
		Notify: func(ctx context.Context, v int64) {
			if err := st.rdb.Publish(ctx, control.SnapshotChannel, v).Err(); err != nil {
				log.WarnContext(ctx, "snapshot ping failed: nodes adopt it on their next poll", slog.String("error", err.Error()))
			}
		},
	}
	var rot *control.Rotator
	if lf, err := creds.OpenLocalFile(strings.TrimPrefix(cfg.CredSource, "localfile://"), cfg.DevKeyFile); err == nil {
		rot = &control.Rotator{Pool: st.pg, Store: lf, Notify: func(ctx context.Context, account string) {
			if err := st.rdb.Publish(ctx, control.CredentialsChannel, account).Err(); err != nil {
				log.WarnContext(ctx, "credential ping failed: nodes reload on their next refresh", slog.String("error", err.Error()))
			}
		}}
	} else {
		log.WarnContext(ctx, "no credential store: rotation endpoints answer 500 until `hubctl creds init-key`", slog.String("error", err.Error()))
	}
	api := adminapi.New(adminapi.Deps{Pool: st.pg, Redis: st.rdb, Verifier: verifier, Publisher: pub, Rotator: rot, Archive: reg.archive,
		Snapshot: reg.store.Snapshot, Log: log})
	mux := http.NewServeMux()
	mux.Handle(adminapi.Base+"/", api.Handler())
	if dev != nil {
		mux.Handle("/.well-known/openid-configuration", dev.Handler())
		mux.Handle("/oidc/", dev.Handler())
	}
	return mux, nil
}

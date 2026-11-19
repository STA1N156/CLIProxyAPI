package executor

import (
	"context"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"sync/atomic"
)

var antigravityClientManager atomic.Pointer[cliproxyauth.Manager]

// StartAntigravityPresence binds the credential manager; each embedded account owns its control plane.
func StartAntigravityPresence(ctx context.Context, _ *config.Config, manager *cliproxyauth.Manager) {
	antigravityClientManager.Store(manager)
	go func() { <-ctx.Done(); helps.CloseAntigravityWire() }()
}

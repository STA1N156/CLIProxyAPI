package auth

import (
	"context"
	"sync/atomic"

	"github.com/tidwall/gjson"
)

type antigravityQuotaFallbackKey struct{}

// Each execution owns its flag; credential and endpoint retries share it.
func withAntigravityQuotaFallback(ctx context.Context) context.Context {
	return context.WithValue(ctx, antigravityQuotaFallbackKey{}, &atomic.Bool{})
}

// ObserveAntigravityQuota429 handles only the exact generic quota message from a 429 response.
func ObserveAntigravityQuota429(ctx context.Context, body []byte) {
	if gjson.GetBytes(body, "error.message").String() != "Resource has been exhausted (e.g. check quota)." {
		return
	}
	if ctx != nil {
		if flag, _ := ctx.Value(antigravityQuotaFallbackKey{}).(*atomic.Bool); flag != nil {
			flag.Store(true)
		}
	}
}

// AntigravityQuotaFallbackEnabled reports whether this request needs prompt relocation on retry.
func AntigravityQuotaFallbackEnabled(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	flag, _ := ctx.Value(antigravityQuotaFallbackKey{}).(*atomic.Bool)
	return flag != nil && flag.Load()
}

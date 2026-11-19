package executor

import (
	"context"
	"encoding/json"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"unicode/utf8"
)

// CountTokens estimates locally, without creating a generation or control request.
func (e *AntigravityExecutor) CountTokens(ctx context.Context, _ *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	model := thinking.ParseSuffix(req.Model).ModelName
	to := sdktranslator.FromString("antigravity")
	payload := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, opts.SourceFormat, to, model, req.Payload, false)
	var count int64
	var visit func(gjson.Result)
	visit = func(node gjson.Result) {
		if node.Type == gjson.String {
			count += int64(max(1, utf8.RuneCountInString(node.String())/4))
			return
		}
		node.ForEach(func(_, child gjson.Result) bool { visit(child); return true })
	}
	visit(gjson.ParseBytes(payload))
	raw, _ := json.Marshal(map[string]int64{"totalTokens": count})
	result := sdktranslator.TranslateTokenCount(ctx, to, cliproxyexecutor.ResponseFormatOrSource(opts), count, raw)
	return cliproxyexecutor.Response{Payload: result}, nil
}

package helps

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type antigravityModelCatalog struct {
	enums  map[string]string
	limits map[string]int
}

//go:embed antigravity_model_limits.json
var antigravityModelLimitsJSON []byte

var antigravityDefaultModelLimits = func() map[string]int {
	var limits map[string]int
	_ = json.Unmarshal(antigravityModelLimitsJSON, &limits)
	return limits
}()

var antigravityModelEnums = cache.NewBoundedLRU[string, *atomic.Pointer[antigravityModelCatalog]](8192, nil)
var antigravitySessionUsage = cache.NewBoundedLRU[string, *atomic.Uint32](16384, nil)
var antigravityExecutions = cache.NewBoundedLRU[string, string](32768, nil)

type antigravityExecutionKey struct{}

// WithAntigravityExecution records the turn identity without retaining its payload.
func WithAntigravityExecution(ctx context.Context, payload []byte) context.Context {
	if key := gjson.GetBytes(payload, "request.labels.request_id").String(); key != "" {
		return context.WithValue(ctx, antigravityExecutionKey{}, key)
	}
	return ctx
}

// CompleteAntigravityExecution records only a terminal, successful model response.
func CompleteAntigravityExecution(ctx context.Context, payload []byte) {
	key, _ := ctx.Value(antigravityExecutionKey{}).(string)
	if key == "" || ctx.Err() != nil {
		return
	}
	response := gjson.GetBytes(payload, "response")
	if !response.Exists() {
		response = gjson.ParseBytes(payload)
	}
	if response.Get("error").Exists() || response.Get("candidates.0.finishReason").String() == "" {
		return
	}
	antigravityExecutions.GetOrAdd(key, uuid.NewString)
}

// CacheAntigravityModelEnums stores an immutable catalog from credential discovery.
func CacheAntigravityModelEnums(scope string, models map[string]string, limits map[string]int) {
	entry := antigravityModelEnums.GetOrAdd(scope, func() *atomic.Pointer[antigravityModelCatalog] {
		return &atomic.Pointer[antigravityModelCatalog]{}
	})
	entry.Store(&antigravityModelCatalog{enums: models, limits: limits})
}

func ApplyAntigravityModelEnum(payload []byte, scope, model string) []byte {
	if entry, ok := antigravityModelEnums.Get(scope); ok {
		if models := entry.Load(); models != nil {
			if value := models.enums[model]; value != "" {
				payload, _ = sjson.SetBytes(payload, "request.labels.model_enum", value)
			}
		}
	}
	return payload
}

func ApplyAntigravityOutputLimit(payload []byte, scope, model string) []byte {
	limit := antigravityDefaultModelLimits[model]
	if entry, ok := antigravityModelEnums.Get(scope); ok {
		if catalog := entry.Load(); catalog != nil && catalog.limits[model] > 0 {
			limit = catalog.limits[model]
		}
	}
	if limit == 0 && !gjson.GetBytes(payload, "request.generationConfig.maxOutputTokens").Exists() {
		modelName := strings.ToLower(model)
		switch {
		case strings.Contains(modelName, "claude"):
			limit = 64000
		case strings.HasPrefix(modelName, "gemini-"):
			limit = 65536
		}
	}
	if limit > 0 {
		payload, _ = sjson.SetBytes(payload, "request.generationConfig.maxOutputTokens", limit)
	}
	return payload
}

// BindAntigravityRequestIdentity links trace labels and step counters to the request history.
func BindAntigravityRequestIdentity(payload []byte, sessionID string, identities ...string) []byte {
	labels := make(map[string]string)
	gjson.GetBytes(payload, "request.labels").ForEach(func(key, value gjson.Result) bool {
		labels[key.String()] = value.String()
		return true
	})
	cascade := strings.TrimSpace(labels["cascade_id"])
	if cascade == "" {
		digest := sha256.Sum256([]byte(sessionID))
		cascade = fmt.Sprintf("%x", digest[:8])
	}
	trajectory := strings.TrimSpace(labels["trajectory_id"])
	if trajectory == "" {
		identity := sessionID
		if len(identities) > 0 && identities[0] != "" {
			identity = identities[0]
		}
		digest := AntigravityIdentityDigest(identity)
		trajectory = uuid.UUID(digest[8:24]).String()
	}
	// Only completed model turns advance the next request step.
	step := 1
	gjson.GetBytes(payload, "request.contents").ForEach(func(_, content gjson.Result) bool {
		if role := content.Get("role").String(); role == "model" || role == "assistant" {
			hasResponse := false
			content.Get("parts").ForEach(func(_, part gjson.Result) bool {
				hasResponse = part.Get("functionResponse").Exists() || part.Get("function_response").Exists()
				return !hasResponse
			})
			if !hasResponse {
				step++
			}
		}
		return true
	})
	if supplied, err := strconv.Atoi(labels["last_step_index"]); err == nil && supplied > 0 {
		step = supplied
	}
	labels["cascade_id"], labels["trajectory_id"] = cascade, trajectory
	if strings.TrimSpace(labels["root_cascade_id"]) == "" {
		labels["root_cascade_id"] = cascade
	}
	labels["last_step_index"] = strconv.Itoa(step)
	if labels["model_enum"] == "" {
		labels["model_enum"] = gjson.GetBytes(payload, "model").String()
	}
	if strings.TrimSpace(labels["request_id"]) == "" {
		labels["request_id"] = trajectory + "-" + strconv.Itoa(step)
	}
	if step > 1 && strings.TrimSpace(labels["last_execution_id"]) == "" {
		if previous, ok := antigravityExecutions.Get(trajectory + "-" + strconv.Itoa(step-1)); ok {
			labels["last_execution_id"] = previous
		}
	}
	var used uint32
	model := strings.ToLower(gjson.GetBytes(payload, "model").String())
	if strings.Contains(model, "claude") {
		used |= 1
	}
	if model != "" && !strings.HasPrefix(model, "gemini-") {
		used |= 2
	}
	if labels["used_claude"] == "true" || labels["used_claude_conservative"] == "true" {
		used |= 1
	}
	if labels["used_non_gemini_model"] == "true" {
		used |= 2
	}
	usage := antigravitySessionUsage.GetOrAdd(cascade, func() *atomic.Uint32 { return &atomic.Uint32{} })
	used |= usage.Or(used)
	labels["used_claude"] = strconv.FormatBool(used&1 != 0)
	labels["used_claude_conservative"] = labels["used_claude"]
	labels["used_non_gemini_model"] = strconv.FormatBool(used&2 != 0)
	raw, _ := json.Marshal(labels)
	payload, _ = sjson.SetRawBytes(payload, "request.labels", raw)
	payload, _ = sjson.SetBytes(payload, "requestId", fmt.Sprintf("agent/%s/%d/%s/%d", cascade, time.Now().UnixMilli(), trajectory, step))
	return payload
}

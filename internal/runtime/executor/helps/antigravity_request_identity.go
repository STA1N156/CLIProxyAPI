package helps

import (
	"crypto/sha256"
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

var antigravityModelEnums = cache.NewBoundedLRU[string, *atomic.Pointer[map[string]string]](8192, nil)
var antigravitySessionUsage = cache.NewBoundedLRU[string, *atomic.Uint32](16384, nil)

// CacheAntigravityModelEnums stores an immutable catalog from credential discovery.
func CacheAntigravityModelEnums(scope string, models map[string]string) {
	entry := antigravityModelEnums.GetOrAdd(scope, func() *atomic.Pointer[map[string]string] {
		return &atomic.Pointer[map[string]string]{}
	})
	entry.Store(&models)
}

func ApplyAntigravityModelEnum(payload []byte, scope, model string) []byte {
	if entry, ok := antigravityModelEnums.Get(scope); ok {
		if models := entry.Load(); models != nil {
			if value := (*models)[model]; value != "" {
				payload, _ = sjson.SetBytes(payload, "request.labels.model_enum", value)
			}
		}
	}
	return payload
}

func antigravityTraceUUID(kind, identity string) string {
	digest := sha256.Sum256([]byte("antigravity:" + kind + ":" + identity))
	id := uuid.UUID(digest[:16])
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return id.String()
}

// BindAntigravityRequestIdentity links trace labels and step counters to the request history.
func BindAntigravityRequestIdentity(payload []byte, sessionID string) []byte {
	labels := make(map[string]string)
	gjson.GetBytes(payload, "request.labels").ForEach(func(key, value gjson.Result) bool {
		labels[key.String()] = value.String()
		return true
	})
	cascade := strings.TrimSpace(labels["cascade_id"])
	if cascade == "" {
		cascade = antigravityTraceUUID("cascade", sessionID)
	}
	trajectory := strings.TrimSpace(labels["trajectory_id"])
	if trajectory == "" {
		trajectory = antigravityTraceUUID("trajectory", cascade)
	}
	// A planner reply is one model request; each tool result is a separate step.
	steps, requests := 0, 0
	gjson.GetBytes(payload, "request.contents").ForEach(func(_, content gjson.Result) bool {
		responses := 0
		content.Get("parts").ForEach(func(_, part gjson.Result) bool {
			if part.Get("functionResponse").Exists() {
				responses++
			}
			return true
		})
		steps += max(1, responses)
		if role := content.Get("role").String(); responses == 0 && (role == "model" || role == "assistant") {
			requests++
		}
		return true
	})
	step, err := strconv.Atoi(labels["last_step_index"])
	if err != nil || step < 0 {
		step = max(0, steps-1)
	}
	labels["cascade_id"], labels["trajectory_id"] = cascade, trajectory
	if strings.TrimSpace(labels["root_cascade_id"]) == "" {
		labels["root_cascade_id"] = cascade
	}
	labels["last_step_index"] = strconv.Itoa(step)
	if strings.TrimSpace(labels["request_id"]) == "" {
		labels["request_id"] = trajectory + "-" + strconv.Itoa(requests)
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
	payload, _ = sjson.SetBytes(payload, "requestId", fmt.Sprintf("agent/%s/%d/%s/%d", cascade, time.Now().UnixMilli(), trajectory, step+1))
	return payload
}

package helps

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// BindAntigravityRequestIdentity uses the same seven labels as agy2api.
func BindAntigravityRequestIdentity(payload []byte, sessionID string, identities ...string) []byte {
	identity := ""
	if len(identities) > 0 {
		identity = identities[0]
	}
	trajectory := uuid.NewString()
	if identity != "" {
		digest := AntigravityIdentityDigest(identity)
		trajectory = uuid.UUID(digest[8:24]).String()
	}
	step := 1
	gjson.GetBytes(payload, "request.contents").ForEach(func(_, content gjson.Result) bool {
		if content.Get("role").String() == "model" {
			step++
		}
		return true
	})
	model := gjson.GetBytes(payload, "model").String()
	lower := strings.ToLower(model)
	claude := strings.Contains(lower, "claude")
	labels := map[string]string{
		"last_step_index":          strconv.Itoa(step),
		"model_enum":               model,
		"request_id":               trajectory + "-" + strconv.Itoa(step),
		"trajectory_id":            trajectory,
		"used_claude":              strconv.FormatBool(claude),
		"used_claude_conservative": strconv.FormatBool(claude),
		"used_non_gemini_model":    strconv.FormatBool(claude || strings.Contains(lower, "gpt-oss") || strings.Contains(lower, "grok")),
	}
	raw, _ := json.Marshal(labels)
	payload, _ = sjson.SetRawBytes(payload, "request.labels", raw)
	digest := sha256.Sum256([]byte(sessionID))
	payload, _ = sjson.SetBytes(payload, "requestId", fmt.Sprintf("agent/%x/%d/%s/%d", digest[:8], time.Now().UnixMilli(), trajectory, step))
	return payload
}

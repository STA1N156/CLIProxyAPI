package helps

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// BindAntigravityRequestIdentity mirrors the handoff client's agent labels.
func BindAntigravityRequestIdentity(payload []byte, sessionID string, identities ...string) []byte {
	identity := ""
	if len(identities) > 0 {
		identity = identities[0]
	}
	trajectory := uuid.NewString()
	if identity != "" {
		digest := AntigravityIdentityDigest(identity)
		id := uuid.UUID(digest[8:24])
		id[6] = (id[6] & 0x0f) | 0x40
		id[8] = (id[8] & 0x3f) | 0x80
		trajectory = id.String()
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
	digest := sha256.Sum256([]byte(trajectory))
	cascade := antigravityUUID(digest[:16])
	labelIndex := step - 1
	labels := map[string]string{
		"cascade_id":               cascade,
		"root_cascade_id":          cascade,
		"last_step_index":          strconv.Itoa(labelIndex),
		"model_enum":               antigravityModelEnum(model),
		"request_id":               trajectory + "-" + strconv.Itoa(labelIndex),
		"trajectory_id":            trajectory,
		"used_claude":              strconv.FormatBool(claude),
		"used_claude_conservative": strconv.FormatBool(claude),
		"used_non_gemini_model":    strconv.FormatBool(claude || strings.Contains(lower, "gpt-oss") || strings.Contains(lower, "grok")),
	}
	if labelIndex > 0 {
		last := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", trajectory, labelIndex)))
		labels["last_execution_id"] = antigravityUUID(last[:16])
	}
	raw, _ := json.Marshal(labels)
	payload, _ = sjson.SetRawBytes(payload, "request.labels", raw)
	payload, _ = sjson.SetBytes(payload, "requestId", fmt.Sprintf("agent/%s/%d/%s/%d", cascade, time.Now().UnixMilli(), trajectory, step))
	return payload
}

func antigravityUUID(raw []byte) string {
	var id uuid.UUID
	copy(id[:], raw)
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return id.String()
}

// AntigravityAccountSessionID models one stable client installation per account.
func AntigravityAccountSessionID(account string) string {
	digest := sha256.Sum256([]byte("antigravity-session\x1e" + account))
	var value uint64
	for _, b := range digest[:8] {
		value = value<<8 | uint64(b)
	}
	return "-" + strconv.FormatUint(value&0x7fffffffffffffff, 10)
}

var antigravityModelEnums = map[string]string{
	"gemini-3-flash": "MODEL_PLACEHOLDER_M18", "gemini-3.1-flash-lite": "MODEL_PLACEHOLDER_M50",
	"gemini-3.5-flash-lite": "MODEL_PLACEHOLDER_M198", "gemini-2.5-flash": "MODEL_GOOGLE_GEMINI_2_5_FLASH",
	"gemini-2.5-flash-lite": "MODEL_GOOGLE_GEMINI_2_5_FLASH_LITE",
	"gemini-4-argon-low":    "MODEL_PLACEHOLDER_M419", "gemini-4-argon-medium": "MODEL_PLACEHOLDER_M418", "gemini-4-argon-high": "MODEL_PLACEHOLDER_M417",
	"gemini-4-hydrogen-low": "MODEL_PLACEHOLDER_M527", "gemini-4-hydrogen-medium": "MODEL_PLACEHOLDER_M526", "gemini-4-hydrogen-high": "MODEL_PLACEHOLDER_M525",
	"gemini-4-hydrogen-xhigh": "MODEL_PLACEHOLDER_M528", "gemini-4-hydrogen-tiered-high": "MODEL_PLACEHOLDER_M530", "gemini-4-hydrogen-tiered-xhigh": "MODEL_PLACEHOLDER_M531",
}

func antigravityModelEnum(model string) string {
	if value := antigravityModelEnums[model]; value != "" {
		return value
	}
	return model
}

// WrapAntigravityUserRequest changes only the latest plain user text, not tool results.
func WrapAntigravityUserRequest(payload []byte, now time.Time) []byte {
	if os.Getenv("ANTIGRAVITY_USER_REQUEST_ENVELOPE") == "0" {
		return payload
	}
	contents := gjson.GetBytes(payload, "request.contents").Array()
	if len(contents) == 0 {
		return payload
	}
	index := len(contents) - 1
	last := contents[index]
	if last.Get("role").String() != "user" {
		return payload
	}
	parts := last.Get("parts").Array()
	for _, part := range parts {
		if part.Get("functionResponse").Exists() || part.Get("function_response").Exists() {
			return payload
		}
	}
	for partIndex, part := range parts {
		text := part.Get("text")
		if text.Type != gjson.String || text.String() == "" {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(text.String()), "<USER_REQUEST>") {
			return payload
		}
		wrapped := "<USER_REQUEST>\n" + text.String() + "\n</USER_REQUEST>\n<ADDITIONAL_METADATA>\nThe current local time is: " + now.Format(time.RFC3339) + ".\n</ADDITIONAL_METADATA>"
		payload, _ = sjson.SetBytes(payload, fmt.Sprintf("request.contents.%d.parts.%d.text", index, partIndex), wrapped)
		break
	}
	return payload
}

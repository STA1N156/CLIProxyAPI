package dataintegration

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func enrichNativeMetadata(payload []byte, path, headerSessionID string) ([]byte, error) {
	if !gjson.ParseBytes(payload).IsObject() {
		return payload, nil
	}

	enriched := payload
	if jsonString(payload, "model") == "" && jsonString(payload, "model_name") == "" {
		if model := modelFromPath(path); model != "" {
			var err error
			enriched, err = sjson.SetBytes(enriched, "model", model)
			if err != nil {
				return nil, err
			}
		}
	}

	if jsonString(payload, "session_id") != "" {
		return enriched, nil
	}
	sessionID := nativeSessionID(payload, headerSessionID)
	if sessionID == "" {
		return enriched, nil
	}
	return sjson.SetBytes(enriched, "session_id", sessionID)
}

func modelFromPath(path string) string {
	const marker = "/models/"
	start := strings.Index(path, marker)
	if start < 0 {
		return ""
	}
	model := path[start+len(marker):]
	if end := strings.IndexByte(model, ':'); end >= 0 {
		model = model[:end]
	}
	return strings.TrimSpace(model)
}

func nativeSessionID(payload []byte, headerSessionID string) string {
	for _, path := range []string{"sessionId", "metadata.session_id", "conversation_id"} {
		if value := jsonString(payload, path); value != "" {
			return value
		}
	}

	userID := jsonString(payload, "metadata.user_id")
	if strings.HasPrefix(userID, "{") {
		if value := strings.TrimSpace(gjson.Get(userID, "session_id").String()); value != "" {
			return value
		}
	}
	return strings.TrimSpace(headerSessionID)
}

func jsonString(payload []byte, path string) string {
	return strings.TrimSpace(gjson.GetBytes(payload, path).String())
}

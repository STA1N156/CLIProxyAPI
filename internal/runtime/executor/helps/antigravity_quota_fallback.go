package helps

import (
	"encoding/json"
	"strconv"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// MoveAntigravitySystemInstructionToUser preserves parts, including images and tools,
// while removing both supported spellings of the system instruction.
func MoveAntigravitySystemInstructionToUser(payload []byte) []byte {
	request := gjson.GetBytes(payload, "request")
	var parts []json.RawMessage
	for _, field := range []string{"systemInstruction", "system_instruction"} {
		for _, part := range request.Get(field + ".parts").Array() {
			parts = append(parts, json.RawMessage(part.Raw))
		}
	}
	if len(parts) == 0 {
		return payload
	}
	contents := request.Get("contents").Array()
	userIndex := -1
	for i, content := range contents {
		if content.Get("role").String() == "user" {
			userIndex = i
			break
		}
	}
	var updated []byte
	var errSet error
	if userIndex >= 0 {
		parts = append(parts, json.RawMessage(`{"text":"\n\n"}`))
		for _, part := range contents[userIndex].Get("parts").Array() {
			parts = append(parts, json.RawMessage(part.Raw))
		}
		updated, errSet = sjson.SetBytes(payload, "request.contents."+strconv.Itoa(userIndex)+".parts", parts)
	} else {
		user, errMarshal := json.Marshal(struct {
			Role  string            `json:"role"`
			Parts []json.RawMessage `json:"parts"`
		}{Role: "user", Parts: parts})
		if errMarshal != nil {
			return payload
		}
		messages := []json.RawMessage{user}
		for _, content := range contents {
			messages = append(messages, json.RawMessage(content.Raw))
		}
		updated, errSet = sjson.SetBytes(payload, "request.contents", messages)
	}
	if errSet != nil {
		return payload
	}
	updated, _ = sjson.DeleteBytes(updated, "request.systemInstruction")
	updated, _ = sjson.DeleteBytes(updated, "request.system_instruction")
	return updated
}

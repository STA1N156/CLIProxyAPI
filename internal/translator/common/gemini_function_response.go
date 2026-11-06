package common

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ContainsJSONRef detects references that upstream would mistake for media references.
func ContainsJSONRef(value gjson.Result) bool {
	if !value.IsObject() && !value.IsArray() {
		return false
	}
	found := false
	value.ForEach(func(key, child gjson.Result) bool {
		found = (value.IsObject() && key.String() == "$ref" && child.Type == gjson.String) || ContainsJSONRef(child)
		return !found
	})
	return found
}

// SetGeminiFunctionResponseResult preserves JSON with schema references as opaque text.
func SetGeminiFunctionResponseResult(part []byte, path string, result gjson.Result) []byte {
	if !result.Exists() {
		part, _ = sjson.SetBytes(part, path, "")
	} else if ContainsJSONRef(result) {
		if strings.HasSuffix(path, "response") {
			path += ".result"
		}
		part, _ = sjson.SetBytes(part, path, result.Raw)
	} else {
		part, _ = sjson.SetRawBytes(part, path, []byte(result.Raw))
	}
	return part
}

func SetGeminiFunctionResponseRaw(part []byte, path, rawJSON string) []byte {
	return SetGeminiFunctionResponseResult(part, path, gjson.Parse(strings.TrimSpace(rawJSON)))
}

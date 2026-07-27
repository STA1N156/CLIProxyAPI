package managementasset

import (
	"bytes"
	_ "embed"
)

const (
	// DataIntegrationScriptPath is the browser asset injected into management.html.
	DataIntegrationScriptPath = "/data-integration.js"
	dataIntegrationMarker     = `data-cpa-feature="data-integration"`
)

//go:embed data_integration.js
var dataIntegrationScript []byte

// DataIntegrationScript returns the embedded panel extension.
func DataIntegrationScript() []byte {
	return dataIntegrationScript
}

// InjectDataIntegration adds the panel extension before any management bundle
// executes, allowing it to reuse the existing authenticated connection.
func InjectDataIntegration(document []byte) []byte {
	if len(document) == 0 || bytes.Contains(document, []byte(dataIntegrationMarker)) {
		return document
	}
	tag := []byte(`<script ` + dataIntegrationMarker + ` src=".` + DataIntegrationScriptPath + `"></script>`)
	lowerDocument := bytes.ToLower(document)
	headIndex := bytes.Index(lowerDocument, []byte("<head"))
	if headIndex < 0 {
		return append(tag, document...)
	}
	headEnd := bytes.IndexByte(document[headIndex:], '>')
	if headEnd < 0 {
		return append(tag, document...)
	}
	insertAt := headIndex + headEnd + 1
	output := make([]byte, 0, len(document)+len(tag))
	output = append(output, document[:insertAt]...)
	output = append(output, tag...)
	output = append(output, document[insertAt:]...)
	return output
}

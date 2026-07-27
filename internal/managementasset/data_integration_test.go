package managementasset

import (
	"bytes"
	"testing"
)

func TestInjectDataIntegration(t *testing.T) {
	t.Parallel()
	document := []byte("<!doctype html><html><head><title>Management</title></head><body></body></html>")
	injected := InjectDataIntegration(document)
	if !bytes.Contains(injected, []byte(dataIntegrationMarker)) {
		t.Fatalf("injected document does not contain data integration marker")
	}
	if bytes.Index(injected, []byte(dataIntegrationMarker)) > bytes.Index(injected, []byte("<title>")) {
		t.Fatalf("data integration script must run before the management bundle")
	}
	if second := InjectDataIntegration(injected); !bytes.Equal(second, injected) {
		t.Fatalf("second injection must be idempotent")
	}
	if len(DataIntegrationScript()) == 0 {
		t.Fatalf("embedded data integration script is empty")
	}
}

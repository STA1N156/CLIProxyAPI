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
	for _, control := range [][]byte{
		[]byte(`id="cpa-di-layout"`),
		[]byte(`value="contract"`),
		[]byte(`id="cpa-di-message-field"`),
		[]byte(`value="messages"`),
		[]byte(`value="conversation"`),
		[]byte(`value="trajectory"`),
		[]byte(`id="cpa-di-clear"`),
		[]byte(`method: "DELETE"`),
		[]byte(`confirm: "CLEAR_ALL_DATA"`),
		[]byte(`id="cpa-di-schema-backfill"`),
		[]byte(`id="cpa-di-schema-import"`),
		[]byte(`id="cpa-di-schema-definition"`),
		[]byte(`/data-integration/tool-schemas/backfill`),
		[]byte(`/data-integration/tool-schemas/import`),
		[]byte(`method: "PUT"`),
		[]byte(`tool_schema_complete_count`),
		[]byte(`个完整工具`),
	} {
		if !bytes.Contains(DataIntegrationScript(), control) {
			t.Fatalf("data integration script is missing %s", control)
		}
	}
}

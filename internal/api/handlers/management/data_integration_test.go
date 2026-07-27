package management

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/dataintegration"
)

func TestDataIntegrationStatsAndDownloadHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, errStore := dataintegration.NewStore(t.TempDir())
	if errStore != nil {
		t.Fatalf("NewStore() error = %v", errStore)
	}
	payload := []byte(`{"messages":[{"role":"user","content":"one"},{"role":"assistant","content":"two"},{"role":"user","content":"three"},{"role":"assistant","content":"four"}]}`)
	if _, errRecord := store.Record("/v1/chat/completions", "request-1", payload); errRecord != nil {
		t.Fatalf("Record() error = %v", errRecord)
	}
	if errClose := store.Close(context.Background()); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}
	handler := &Handler{dataIntegrationStore: store}

	statsResponse := httptest.NewRecorder()
	statsContext, _ := gin.CreateTestContext(statsResponse)
	statsContext.Request = httptest.NewRequest(
		http.MethodGet,
		"/v0/management/data-integration/stats?criteria=effective_turns,first_role",
		nil,
	)
	handler.GetDataIntegrationStats(statsContext)
	if statsResponse.Code != http.StatusOK {
		t.Fatalf("stats status = %d, body = %s", statsResponse.Code, statsResponse.Body.String())
	}

	downloadResponse := httptest.NewRecorder()
	downloadContext, _ := gin.CreateTestContext(downloadResponse)
	downloadContext.Request = httptest.NewRequest(
		http.MethodGet,
		"/v0/management/data-integration/download?criteria=effective_turns,first_role&count=1&format=json&layout=contract&message_field=conversation",
		nil,
	)
	handler.DownloadDataIntegrationZIP(downloadContext)
	if downloadResponse.Code != http.StatusOK {
		t.Fatalf("download status = %d, body = %s", downloadResponse.Code, downloadResponse.Body.String())
	}
	reader, errReader := zip.NewReader(bytes.NewReader(downloadResponse.Body.Bytes()), int64(downloadResponse.Body.Len()))
	if errReader != nil {
		t.Fatalf("download is not a valid zip: %v", errReader)
	}
	if len(reader.File) != 2 || reader.File[1].Name != "sessions/000001.json" {
		t.Fatalf("unexpected zip contents: %+v", reader.File)
	}
	session, errOpen := reader.File[1].Open()
	if errOpen != nil {
		t.Fatalf("open exported session: %v", errOpen)
	}
	defer func() {
		_ = session.Close()
	}()
	var exported map[string]any
	if errDecode := json.NewDecoder(session).Decode(&exported); errDecode != nil {
		t.Fatalf("decode exported session: %v", errDecode)
	}
	if _, exists := exported["conversation"]; !exists {
		t.Fatal("contract export is missing the selected conversation field")
	}
	if _, exists := exported["messages"]; exists {
		t.Fatal("contract export must use only the selected message field")
	}
}

func TestDataIntegrationTimeRangeValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	now := time.Now().UTC()
	ginContext.Request = httptest.NewRequest(
		http.MethodGet,
		"/?from="+now.Add(time.Hour).Format(time.RFC3339)+"&to="+now.Format(time.RFC3339),
		nil,
	)
	if _, errRange := dataIntegrationTimeRange(ginContext); errRange == nil {
		t.Fatalf("expected reversed time range to fail")
	}
}

func TestClearDataIntegrationRequiresConfirmation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, errStore := dataintegration.NewStore(t.TempDir())
	if errStore != nil {
		t.Fatalf("NewStore() error = %v", errStore)
	}
	if _, errRecord := store.Record("/v1/responses", "request-1", []byte(`{"input":"hello"}`)); errRecord != nil {
		t.Fatalf("Record() error = %v", errRecord)
	}
	handler := &Handler{dataIntegrationStore: store}

	rejectedResponse := httptest.NewRecorder()
	rejectedContext, _ := gin.CreateTestContext(rejectedResponse)
	rejectedContext.Request = httptest.NewRequest(
		http.MethodDelete,
		"/v0/management/data-integration",
		bytes.NewBufferString(`{"confirm":"no"}`),
	)
	rejectedContext.Request.Header.Set("Content-Type", "application/json")
	handler.ClearDataIntegration(rejectedContext)
	if rejectedResponse.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed clear status = %d, want 400", rejectedResponse.Code)
	}

	clearResponse := httptest.NewRecorder()
	clearContext, _ := gin.CreateTestContext(clearResponse)
	clearContext.Request = httptest.NewRequest(
		http.MethodDelete,
		"/v0/management/data-integration",
		bytes.NewBufferString(`{"confirm":"CLEAR_ALL_DATA"}`),
	)
	clearContext.Request.Header.Set("Content-Type", "application/json")
	handler.ClearDataIntegration(clearContext)
	if clearResponse.Code != http.StatusOK {
		t.Fatalf("confirmed clear status = %d, body = %s", clearResponse.Code, clearResponse.Body.String())
	}
	var result dataintegration.ClearResult
	if errDecode := json.Unmarshal(clearResponse.Body.Bytes(), &result); errDecode != nil {
		t.Fatalf("decode clear response: %v", errDecode)
	}
	if result.RemovedRequests != 1 {
		t.Fatalf("removed requests = %d, want 1", result.RemovedRequests)
	}
	if errClose := store.Close(context.Background()); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}
}

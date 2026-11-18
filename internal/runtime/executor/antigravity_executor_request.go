package executor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func (e *AntigravityExecutor) buildRequest(ctx context.Context, auth *cliproxyauth.Auth, token, modelName string, payload []byte, stream bool, alt, baseURL string, sessionIdentities ...string) (*http.Request, error) {
	if token == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing access token"}
	}

	base := strings.TrimSuffix(baseURL, "/")
	if base == "" {
		base = buildBaseURL(auth)
	}
	path := antigravityGeneratePath
	if stream {
		path = antigravityStreamPath
	}
	var requestURL strings.Builder
	requestURL.WriteString(base)
	requestURL.WriteString(path)
	if stream {
		if alt != "" {
			requestURL.WriteString("?$alt=")
			requestURL.WriteString(url.QueryEscape(alt))
		} else {
			requestURL.WriteString("?alt=sse")
		}
	} else if alt != "" {
		requestURL.WriteString("?$alt=")
		requestURL.WriteString(url.QueryEscape(alt))
	}

	projectID, errProject := e.projectIDForRequest(ctx, auth, token)
	if errProject != nil {
		return nil, errProject
	}
	payload = helps.NormalizeAntigravityThinking(payload)
	payload = geminiToAntigravity(modelName, payload, projectID, sessionIdentities...)
	reqType := util.GetGJSONBytesNoCopy(payload, "requestType").String()
	if reqType == "chat" || reqType == "agent" {
		payload = helps.ApplyAntigravityModelEnum(payload, antigravityTransportScope(auth), modelName)
	}
	if cliproxyauth.AntigravityQuotaFallbackEnabled(ctx) {
		payload = helps.MoveAntigravitySystemInstructionToUser(payload)
	}

	payload = helps.ApplyAntigravityOutputLimit(payload, antigravityTransportScope(auth), modelName)

	mode := util.GetGJSONBytesNoCopy(payload, "request.toolConfig.functionCallingConfig.mode").String()
	if antigravityRequestNeedsSchemaSanitization(payload) {
		payload = []byte(sanitizeAntigravityRequestSchemas(string(payload), strings.EqualFold(mode, "VALIDATED")))
	}
	payload = helps.SplitAntigravityFunctionTools(payload)
	if strings.Contains(strings.ToLower(modelName), "claude") {
		payload = helps.NormalizeAntigravityClaudeTools(payload)
		const budgetPath = "request.generationConfig.thinkingConfig.thinkingBudget"
		if strings.EqualFold(mode, "ANY") {
			payload = helps.DeleteJSONField(payload, "request.generationConfig.thinkingConfig")
		} else if budget := util.GetGJSONBytesNoCopy(payload, budgetPath); budget.Type == gjson.Number && budget.Num > 0 && budget.Num < 1024 {
			payload, _ = sjson.SetBytes(payload, budgetPath, 1024)
		}
	}
	payload = applyAntigravityNativeSignatureReplayIfNeeded(modelName, payload)
	if reqType == "agent" || reqType == "chat" {
		payload = helps.WrapAntigravityUserMessages(payload)
	}
	payload = helps.OrderAntigravityJSON(payload)
	var payloadLog []byte
	if e.cfg != nil && e.cfg.RequestLog {
		payloadLog = append([]byte(nil), payload...)
	}

	ctx = helps.WithAntigravityExecution(ctx, payload)
	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), bytes.NewReader(payload))
	if errReq != nil {
		return nil, errReq
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("User-Agent", resolveUserAgent(auth))
	if host := resolveHost(base); host != "" {
		httpReq.Host = host
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	applyAntigravityRequestHeaders(httpReq, attrs)

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       requestURL.String(),
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      payloadLog,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	return httpReq, nil
}

func applyAntigravityRequestHeaders(req *http.Request, attrs map[string]string) {
	// Only tracing headers may be forwarded from the downstream client.
	var tracing map[string]string
	for key, value := range attrs {
		if strings.HasPrefix(key, "header:") && misc.IsAntigravityTraceHeader(strings.TrimPrefix(key, "header:")) {
			if tracing == nil {
				tracing = make(map[string]string)
			}
			tracing[key] = value
		}
	}
	util.ApplyCustomHeadersFromAttrs(req, tracing)
	misc.SanitizeAntigravityHeaders(req)
}

// sanitizeAntigravityRequestSchemas cleans the JSON schemas carried by an Antigravity request.
//
// Cleaning is applied only to the payload locations that actually hold a JSON schema. The schema
// cleaner rewrites keys such as "title", "format", "default" and "const", which are also ordinary
// data keys inside functionCall arguments replayed from conversation history. Running it over the
// whole document silently mutated that history, so tools lost required argument fields and the
// model imitated the corrupted examples on later turns.
func sanitizeAntigravityRequestSchemas(payloadStr string, requirePlaceholder bool) string {
	for _, base := range antigravityFunctionDeclarationPaths(payloadStr) {
		oldPath := base + ".parametersJsonSchema"
		if !gjson.Get(payloadStr, oldPath).Exists() {
			continue
		}
		renamed, errRename := util.RenameKey(payloadStr, oldPath, base+".parameters")
		if errRename != nil {
			log.Debugf("antigravity: failed to rename %s: %v", oldPath, errRename)
			continue
		}
		payloadStr = renamed
	}

	toolSchemaCleaner := func(schema string) string {
		return util.CleanJSONSchemaForAntigravityTool(schema, requirePlaceholder)
	}
	responseSchemaCleaner := util.CleanJSONSchemaForAntigravityResponse

	cleanNestedToolSchema := func(schemaRaw string) string {
		return helps.UppercaseAntigravitySchemaTypes(cleanNestedSchema(toolSchemaCleaner, schemaRaw))
	}
	payloadStr = cleanAntigravitySchemasAtPaths(payloadStr, antigravityDeclarationSchemaPaths(payloadStr), cleanNestedToolSchema)
	payloadStr = cleanAntigravitySchemasAtPaths(payloadStr, antigravityGenerationSchemaPaths(payloadStr), responseSchemaCleaner)
	return payloadStr
}

func cleanAntigravitySchemasAtPaths(payloadStr string, schemaPaths []string, clean func(string) string) string {
	for _, schemaPath := range schemaPaths {
		schema := gjson.Get(payloadStr, schemaPath)
		if !schema.Exists() {
			continue
		}
		updated, errSet := sjson.SetRawBytes([]byte(payloadStr), schemaPath, []byte(clean(schema.Raw)))
		if errSet != nil {
			log.Debugf("antigravity: failed to write cleaned schema at %s: %v", schemaPath, errSet)
			continue
		}
		payloadStr = string(updated)
	}
	return payloadStr
}

// antigravitySchemaWrapperKey nests a schema during cleaning. It is never sent upstream.
const antigravitySchemaWrapperKey = "schema"

// cleanNestedSchema cleans a schema with it nested one level down, then unwraps it.
//
// The cleaner deliberately skips placeholder insertion for a top-level schema, but Claude's
// VALIDATED mode needs every tool schema to declare at least one required property. Whole-payload
// cleaning always saw tool schemas nested inside the request, so nesting is reproduced here to keep
// the emitted schema byte-identical to the previous behaviour.
func cleanNestedSchema(clean func(string) string, schemaRaw string) string {
	wrapped, errWrap := sjson.SetRaw("{}", antigravitySchemaWrapperKey, schemaRaw)
	if errWrap != nil {
		return clean(schemaRaw)
	}
	if unwrapped := gjson.Get(clean(wrapped), antigravitySchemaWrapperKey); unwrapped.Exists() {
		return unwrapped.Raw
	}
	return clean(schemaRaw)
}

// antigravityFunctionDeclarationPaths returns the path of every function declaration in the request.
// Both the camelCase and snake_case spellings are accepted because callers reach this executor
// through different translators.
func antigravityFunctionDeclarationPaths(payloadStr string) []string {
	tools := gjson.Get(payloadStr, "request.tools")
	if !tools.IsArray() {
		return nil
	}
	paths := make([]string, 0, len(tools.Array()))
	for i, tool := range tools.Array() {
		for _, declKey := range []string{"functionDeclarations", "function_declarations"} {
			decls := tool.Get(declKey)
			if !decls.IsArray() {
				continue
			}
			for j := range decls.Array() {
				paths = append(paths, fmt.Sprintf("request.tools.%d.%s.%d", i, declKey, j))
			}
		}
	}
	return paths
}

// antigravitySchemaPaths returns every payload path that holds a JSON schema document.
// A function declaration may carry a schema for its parameters and for its result, so all of
// them must be cleaned; anything omitted here reaches the upstream API uncleaned.
func antigravitySchemaPaths(payloadStr string) []string {
	paths := antigravityDeclarationSchemaPaths(payloadStr)
	return append(paths, antigravityGenerationSchemaPaths(payloadStr)...)
}

func antigravityDeclarationSchemaPaths(payloadStr string) []string {
	paths := make([]string, 0, 8)
	for _, base := range antigravityFunctionDeclarationPaths(payloadStr) {
		for _, key := range antigravityDeclarationSchemaKeys {
			if gjson.Get(payloadStr, base+"."+key).IsObject() {
				paths = append(paths, base+"."+key)
			}
		}
	}
	return paths
}

func antigravityGenerationSchemaPaths(payloadStr string) []string {
	paths := make([]string, 0, len(antigravityGenerationConfigContainers)*len(antigravityGenerationSchemaKeys))
	for _, container := range antigravityGenerationConfigContainers {
		for _, key := range antigravityGenerationSchemaKeys {
			path := container + "." + key
			if gjson.Get(payloadStr, path).IsObject() {
				paths = append(paths, path)
			}
		}
	}
	return paths
}

// The upstream API is proto-JSON and accepts either spelling, and the Gemini translator forwards
// whichever one the client sent. Both are therefore cleaned where they sit rather than renamed:
// renaming would alter the body the client asked for, and only the unsupported keywords inside a
// schema cause upstream errors. The one exception is parametersJsonSchema, renamed onto parameters
// above because whole-payload cleaning did the same.
var (
	antigravityDeclarationSchemaKeys = []string{
		"parameters", "parametersJsonSchema", "parameters_json_schema",
		"response", "responseJsonSchema", "response_json_schema",
	}
	antigravityGenerationConfigContainers = []string{
		"request.generationConfig", "request.generation_config",
	}
	antigravityGenerationSchemaKeys = []string{
		"responseSchema", "responseJsonSchema", "response_schema", "response_json_schema",
	}
)

func antigravityRequestNeedsSchemaSanitization(payload []byte) bool {
	if gjson.GetBytes(payload, "request.tools.0").Exists() {
		return true
	}
	for _, container := range antigravityGenerationConfigContainers {
		for _, key := range antigravityGenerationSchemaKeys {
			if gjson.GetBytes(payload, container+"."+key).Exists() {
				return true
			}
		}
	}
	return false
}
func buildBaseURL(auth *cliproxyauth.Auth) string {
	if baseURLs := antigravityBaseURLFallbackOrder(auth); len(baseURLs) > 0 {
		return baseURLs[0]
	}
	return antigravityBaseURLDaily
}

func antigravityLoadCodeAssistBaseURL(auth *cliproxyauth.Auth) string {
	return buildBaseURL(auth)
}

func resolveHost(base string) string {
	parsed, errParse := url.Parse(base)
	if errParse != nil {
		return ""
	}
	if parsed.Host != "" {
		return parsed.Host
	}
	return strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
}

func resolveUserAgent(auth *cliproxyauth.Auth) string {
	return misc.AntigravityRequestUserAgent(antigravityConfiguredUserAgent(auth))
}

func AntigravityUserAgent(auth *cliproxyauth.Auth) string {
	return resolveUserAgent(auth)
}

// CacheAntigravityModelEnums reuses credential discovery for request trace labels.
func CacheAntigravityModelEnums(auth *cliproxyauth.Auth, models map[string]string, limits map[string]int) {
	helps.CacheAntigravityModelEnums(antigravityTransportScope(auth), models, limits)
}

func resolveLoadCodeAssistUserAgent(auth *cliproxyauth.Auth) string {
	return misc.AntigravityLoadCodeAssistUserAgent(antigravityConfiguredUserAgent(auth))
}

func antigravityConfiguredUserAgent(auth *cliproxyauth.Auth) string {
	raw := ""
	if auth != nil {
		if auth.Attributes != nil {
			if ua := strings.TrimSpace(auth.Attributes["user_agent"]); ua != "" {
				raw = ua
			}
		}
		if raw == "" && auth.Metadata != nil {
			if ua, ok := auth.Metadata["user_agent"].(string); ok && strings.TrimSpace(ua) != "" {
				raw = strings.TrimSpace(ua)
			}
		}
	}
	return raw
}

var antigravityBaseURLFallbackOrder = func(auth *cliproxyauth.Auth) []string {
	if base := resolveCustomAntigravityBaseURL(auth); base != "" {
		return []string{base}
	}
	return []string{antigravityBaseURLDaily}
}

func resolveCustomAntigravityBaseURL(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["base_url"]); v != "" {
			return strings.TrimSuffix(v, "/")
		}
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["base_url"].(string); ok {
			v = strings.TrimSpace(v)
			if v != "" {
				return strings.TrimSuffix(v, "/")
			}
		}
	}
	return ""
}

func geminiToAntigravity(modelName string, payload []byte, projectID string, sessionIdentities ...string) []byte {
	template := bytes.Clone(payload)
	template = helps.SetStringIfDifferent(template, "model", modelName)
	template = helps.SetStringIfDifferent(template, "userAgent", "antigravity")
	template = helps.NormalizeAntigravitySystemInstruction(template)
	if toolConfig := util.GetGJSONBytesNoCopy(template, "toolConfig"); toolConfig.Exists() {
		if !util.GetGJSONBytesNoCopy(template, "request.toolConfig").Exists() {
			template, _ = sjson.SetRawBytes(template, "request.toolConfig", []byte(toolConfig.Raw))
		}
		template, _ = sjson.DeleteBytes(template, "toolConfig")
	}
	reqType, hasTools := helps.AntigravityRequestType(template, modelName)
	template = helps.SetStringIfDifferent(template, "requestType", reqType)
	if !hasTools {
		template = helps.DeleteJSONField(template, "request.toolConfig")
		template = helps.DeleteJSONField(template, "request.tools")
	}

	if projectID != "" {
		template = helps.SetStringIfDifferent(template, "project", projectID)
	} else {
		template, _ = sjson.DeleteBytes(template, "project")
	}

	if reqType != "web_search" {
		sessionID := cliproxysession.NormalizeExplicitID(util.GetGJSONBytesNoCopy(template, "request.sessionId").String())
		if sessionID == "" {
			sessionID = cliproxysession.NormalizeExplicitID(util.GetGJSONBytesNoCopy(template, "request.session_id").String())
		}
		identity := sessionID
		if sessionID == "" && len(sessionIdentities) > 0 {
			identity = strings.TrimSpace(sessionIdentities[0])
		}
		if sessionID == "" {
			if identity == "" {
				identity = cliproxysession.DeriveAntigravityID(sdktranslator.FormatAntigravity, template, "", modelName)
			}
			if strings.HasPrefix(identity, "-") {
				sessionID = identity
			} else {
				sessionID = helps.AntigravitySessionID(identity)
			}
			if sessionID == "" {
				sessionID = generateSessionID()
			}
		}
		template, _ = sjson.SetBytes(template, "request.sessionId", sessionID)
		template = helps.BindAntigravityRequestIdentity(template, sessionID, identity)
	}

	template = helps.DeleteJSONField(template, "request.session_id")
	return helps.DeleteJSONField(template, "request.safetySettings")
}

func generateSessionID() string {
	randSourceMutex.Lock()
	n := randSource.Int63n(9_000_000_000_000_000_000)
	randSourceMutex.Unlock()
	return "-" + strconv.FormatInt(n, 10)
}

func antigravityRequestSessionIdentity(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
	if identity := helps.DerivedSessionID(opts.Metadata, req.Metadata); identity != "" {
		return identity
	}
	original := opts.OriginalRequest
	if len(original) == 0 {
		original = req.Payload
	}
	identity := cliproxyauth.ExtractSessionID(opts.Headers, original, opts.Metadata)
	if strings.HasPrefix(identity, "msg:") {
		return ""
	}
	return identity
}

func deriveAntigravityPayloadSessionID(model string, payload []byte) string {
	return helps.AntigravitySessionID(cliproxysession.DeriveAntigravityID(sdktranslator.FormatAntigravity, payload, "", model))
}

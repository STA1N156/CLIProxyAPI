package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

var state = pluginState{
	config: pluginConfig{MaxConcurrency: 2, RejectKeyword: "blocked"},
	active: make(map[string]struct{}),
}

type pluginState struct {
	mu     sync.Mutex
	config pluginConfig
	active map[string]struct{}
}

type pluginConfig struct {
	MaxConcurrency int    `yaml:"max_concurrency"`
	RejectKeyword  string `yaml:"reject_keyword"`
}

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type lifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	RequestInterceptor     bool `json:"request_interceptor"`
	RequestLifecyclePlugin bool `json:"request_lifecycle_plugin"`
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if errConfigure := configure(request); errConfigure != nil {
			return nil, errConfigure
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodRequestInterceptBefore:
		return interceptBeforeAuth(request)
	case pluginabi.MethodRequestInterceptAfter:
		return passThroughRequest(request)
	case pluginabi.MethodRequestComplete:
		return completeRequest(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func configure(raw []byte) error {
	var req lifecycleRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return errUnmarshal
		}
	}
	if req.SchemaVersion < 2 {
		return fmt.Errorf("request lifecycle plugin requires host schema version 2 or newer")
	}
	cfg := pluginConfig{MaxConcurrency: 2, RejectKeyword: "blocked"}
	if len(req.ConfigYAML) > 0 {
		if errUnmarshal := yaml.Unmarshal(req.ConfigYAML, &cfg); errUnmarshal != nil {
			return errUnmarshal
		}
	}
	if cfg.MaxConcurrency < 1 {
		return fmt.Errorf("max_concurrency must be greater than zero")
	}
	cfg.RejectKeyword = strings.TrimSpace(cfg.RejectKeyword)
	state.mu.Lock()
	defer state.mu.Unlock()
	state.config = cfg
	return nil
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "request-lifecycle",
			Version:          "0.1.0",
			Author:           "router-for-me",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			Logo:             "https://raw.githubusercontent.com/router-for-me/CLIProxyAPI/main/docs/logo.png",
			ConfigFields: []pluginapi.ConfigField{
				{
					Name:        "max_concurrency",
					Type:        pluginapi.ConfigFieldTypeInteger,
					Description: "Maximum number of intercepted requests allowed in flight.",
				},
				{
					Name:        "reject_keyword",
					Type:        pluginapi.ConfigFieldTypeString,
					Description: "Terminates requests whose raw JSON body contains this keyword.",
				},
			},
		},
		Capabilities: registrationCapability{
			RequestInterceptor:     true,
			RequestLifecyclePlugin: true,
		},
	}
}

func interceptBeforeAuth(raw []byte) ([]byte, error) {
	var req pluginapi.RequestInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	if req.RequestID == "" {
		return nil, fmt.Errorf("request ID is required")
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if _, exists := state.active[req.RequestID]; exists {
		return okEnvelope(pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body})
	}
	if state.config.RejectKeyword != "" && strings.Contains(string(req.Body), state.config.RejectKeyword) {
		return terminatedResponse(http.StatusForbidden, "request blocked by plugin policy", nil)
	}
	if len(state.active) >= state.config.MaxConcurrency {
		return terminatedResponse(http.StatusTooManyRequests, "plugin concurrency limit reached", http.Header{"Retry-After": {"1"}})
	}
	state.active[req.RequestID] = struct{}{}
	return okEnvelope(pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body})
}

func passThroughRequest(raw []byte) ([]byte, error) {
	var req pluginapi.RequestInterceptRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	return okEnvelope(pluginapi.RequestInterceptResponse{Headers: req.Headers, Body: req.Body})
}

func terminatedResponse(statusCode int, message string, headers http.Header) ([]byte, error) {
	body, errMarshal := json.Marshal(map[string]any{
		"error": map[string]any{
			"type":    "plugin_request_rejected",
			"message": message,
		},
	})
	if errMarshal != nil {
		return nil, errMarshal
	}
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("Content-Type", "application/json")
	return okEnvelope(pluginapi.RequestInterceptResponse{
		Terminate:       true,
		StatusCode:      statusCode,
		ResponseHeaders: headers,
		ResponseBody:    body,
	})
}

func completeRequest(raw []byte) ([]byte, error) {
	var completion pluginapi.RequestCompletion
	if errUnmarshal := json.Unmarshal(raw, &completion); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	delete(state.active, completion.RequestID)
	return okEnvelope(struct{}{})
}

func okEnvelope(v any) ([]byte, error) {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, errMarshal := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	if errMarshal != nil {
		return []byte(`{"ok":false,"error":{"code":"plugin_error","message":"encode error"}}`)
	}
	return raw
}

# Antigravity CLI transport

The measured baseline is Antigravity CLI 1.5.9 for Windows amd64. The official executable's consumer OAuth requests were captured using an isolated credential and use `auth_method=consumer`.

All Antigravity API clients use HTTP/1.1 with ALPN `http/1.1`. OAuth token and Google user information requests retain the Go HTTP user agent. Streaming uses chunked transfer, and generation request IDs use the CLI's path format. Login, onboarding, user information, model discovery, quota queries, generation, streaming, token counting, and management API calls share these transport settings. Explicit caller-provided custom transports and user agents remain operator overrides.

Build with Go 1.27.4 and the TLS source overlay:

```sh
TLS_OVERLAY="$(go run ./cmd/antigravity_tls_overlay)"
go build -overlay="$TLS_OVERLAY" -o cli-proxy-api ./cmd/server
```

For PowerShell:

```powershell
$tlsOverlay = go run ./cmd/antigravity_tls_overlay
go build "-overlay=$tlsOverlay" -o cli-proxy-api.exe ./cmd/server
```

The overlay removes Go 1.27's workaround that omits ML-DSA from the certificate signature algorithm extension. Go's existing ML-DSA implementation and certificate verification remain enabled. It writes temporary source files without changing the installed Go SDK and rejects incompatible source layouts. Docker, release builds, and the build workflow apply it automatically. A plain `go build` omits this adjustment. Use a directly installed Go distribution: Go does not permit overlays inside a toolchain downloaded into its module cache.

The version updater obtains the version from the official Windows CLI manifest. The build identifier is included only for the measured 1.5.9 release. Future releases require a new capture before adding their build identifier. Ordinary generation omits the outer `requestType` field, including an explicitly supplied `agent` value; image and web-search request types remain separate.

Execution uses Go HTTP clients and bounded, credential-isolated connection pools. There are no CLI child processes, ACP session queues, or process-based model discovery. Streaming, tool result replay, thinking output, token counting, and credential retries use the native Go executor. Existing `antigravity.acp` configuration keys are ignored. The client-provided system instruction is retained; CLI-only workspace prompts, checkpoint calls, and internal execution state are not fabricated.

Model capability discovery includes the selected credential's project. Ordinary generation requests link their request ID to `cascade_id`, `trajectory_id`, `root_cascade_id`, `last_step_index`, and `request_id` labels. Missing trace UUIDs are derived from the existing session identity without a cache or database lookup. Complete message history supplies separate counters when the caller does not provide them: each prior model reply advances the label request index, while each tool result counts as its own trajectory step. The outer request ID ends in the last step index plus one. Replaying the same history on credential/prompt retries keeps both counters stable; explicit label request IDs, step indices, trace IDs, and additional labels are preserved. Image generation and independent web-search request formats retain their own request IDs.

The consumer comparison forwarded user information, initialization, model discovery, and one streaming generation request to Google; all returned HTTP 200. The CLI's checkpoint request was answered locally. Other auxiliary requests were inspected locally. The earlier comparison covered the proxy's HTTP and TLS settings with fabricated credentials. Third-party plugin transports are outside this capture.

The same credential discovery request caches official model enums in memory for `labels.model_enum`; generation performs no extra network lookup. Catalog refreshes replace the snapshot and credentials have separate entries (up to 8,192). The three model-usage labels are retained across model switches in a bounded 16,384-session cache, matching the captured CLI behavior. Explicit execution IDs are passed through; the proxy does not invent CLI execution events. No model output-token override is added.

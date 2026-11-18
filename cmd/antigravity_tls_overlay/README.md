# Antigravity Go transport

Generation request formatting follows agy2api commits `ca34911` and `3681773` (Hub 2.28.0). The pinned default User-Agent is `antigravity/hub/2.28.0 (aidev_client; os_type=windows; arch=amd64; cl=1008300195)`. The version/build pair is fixed; no CLI manifest lookup runs in the background.

Execution remains native Go with bounded, credential-isolated connection pools and HTTP/1.1. There are no CLI child processes or ACP queues. TLS settings retain the previously measured Windows CLI 1.5.9 baseline; the Hub formatting update does not add a separately captured Hub TLS profile. OAuth token and user-information calls retain their Go HTTP User-Agent.

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

Generation uses `chat` for text, `agent` for tools/tool history, and `image_gen` for images. Independent `web_search` requests retain their existing path. Empty/orphan tools and toolConfig are removed; supplied tool calling modes are preserved instead of injecting VALIDATED. Functions are split into ordered individual declarations, parameter schema types are uppercase, and missing Claude tool IDs are derived deterministically and paired with tool responses. Tool arguments are not rewritten.

Envelope order is `project`, `requestId`, `request`, `model`, `userAgent`, `requestType`. Inner order is `contents`, `systemInstruction`, `tools`, `toolConfig`, `labels`, `generationConfig`, `sessionId`. Legacy system/thinking field aliases are normalized. Claude forced-tool mode omits thinking, and positive thinking budgets below 1024 are raised to 1024. Explicit topK is retained; no default topK is inserted.

Credential affinity and upstream IDs share the model, full system text, and first two message features. With two messages present, appended history keeps the anchor stable. The first eight hash bytes produce the negative decimal sessionId; bytes 8–24 produce trajectory_id. Current step is completed model turns plus one, shared by requestId and labels. Explicit client session identifiers remain authoritative, and caller credentials isolate otherwise identical conversations. A first request containing only one message acquires its two-message anchor on the next turn, as in the source implementation.

Model discovery includes the credential's project and caches its model enums/output limits in memory. Requests reuse that cache without extra network lookups, with the embedded per-model limits as fallback. Unknown Claude/Gemini models receive family defaults when the client has not supplied a limit. Existing exact Resource-exhausted 429 retry handling and no-cooldown behavior remain intact.

Only protocol headers and permitted trace headers are forwarded. Client-only headers, Connection, and header-level requestId/requestType are stripped. Go manages Content-Length, gzip decoding, header ordering and persistent connections. Sensitive-word settings now remove case-sensitive literal matches from instruction/message text, with a bounded compiled-pattern cache; signatures and tool arguments are preserved.

CPA retains these extensions over the upstream recipe: USER_REQUEST/time wrapping with cached history timestamps; cascade/root labels and per-trajectory model-use tracking; and last_execution_id generated locally after successful completion. These execution IDs are not supplied by the Hub. Unknown fields and explicit trace labels remain supported. HTTP header order/casing comes from Go and can differ from Python's HTTP client.

A local HTTP capture on 2026-11-18 compared six equal-input cases against the unchanged upstream request-building functions: three text turns, Gemini tools, Claude forced tools, and image generation. Request type, envelope/inner order, session/trajectory IDs, generation configuration and tool declarations matched in all six cases. This capture used fabricated credentials and a local endpoint.

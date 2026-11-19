# Antigravity request and transport profile

Antigravity request formatting follows agy2api upstream `3681773` (including `ca34911`). The default data-plane User-Agent is `antigravity/hub/2.28.0 (aidev_client; os_type=windows; arch=amd64; cl=1008300195)`. OAuth and Google user-info use `python-httpx/0.28.1`, matching agy2api's plain HTTPX client.

## Transport

Native Go HTTP/1.1 connection pools remain isolated by credential and proxy. TLS is supplied by uTLS using the captured HTTPX/OpenSSL 3 ClientHello: cipher order, extensions, supported groups, signature algorithms, key share and ALPN. HTTP headers use the corresponding HTTPX order, gzip is decoded by Go, and connections are reused. Direct connections and HTTP CONNECT proxies were checked locally with two requests per connection. Custom TLS dialers remain explicit operator overrides.

The reference transport sample was HTTPX 0.28.1 with Python 3.12.11/OpenSSL 3.0.16. The upstream Dockerfile uses Python 3.13. No official CLI executable or Python process is required by CPA at runtime. Existing build scripts may still apply the legacy standard-library TLS overlay; it does not define this uTLS ClientHello.

## Requests

Text requests use `chat`, function tools or tool history use `agent`, and images use `image_gen`. User messages are forwarded without USER_REQUEST wrappers or timestamps. The envelope order is `project`, `requestId`, `request`, `model`, `userAgent`, `requestType`; the inner order is `contents`, `systemInstruction`, `tools`, `toolConfig`, `labels`, `generationConfig`, `sessionId`.

The seven labels are `last_step_index`, `model_enum`, `request_id`, `trajectory_id`, `used_claude`, `used_claude_conservative` and `used_non_gemini_model`. `model_enum` contains the upstream model name. CPA does not append cascade, root-cascade or last-execution labels. Session and trajectory IDs derive from the model/system/first-two-message anchor; step equals model turns plus one.

Gemini 3/4 and Claude 5.5 route their thinking level through the model name and default to includeThoughts. Explicit thinking models use the upstream 1024-token default. Claude forced tool choice and tool histories suppress incompatible thinking configuration. Output limits use agy2api's defaults (Claude 64000, otherwise 65536), retaining other explicit client values. Ten safety categories use OFF. Images use the upstream image model, candidateCount and imageConfig behavior.

Tool declarations are individually wrapped with uppercase schema types. Missing Claude call IDs are deterministic and paired with results. CPA preserves all declarations when an incoming Claude tool group has multiple functions; the referenced upstream implementation drops declarations after the first. This avoids losing supplied tools.

## Existing runtime behavior

Credential selection, retries, short cooldowns, reasoning replay, proxy settings and connection pooling remain in Go. The exact Resource-exhausted 429 still does not cool down a credential and may relocate system instructions on retry. That retry-only transformation is deliberately retained by user instruction. Model discovery still includes the credential project, but no longer rewrites outgoing model_enum or maxOutputTokens from a credential catalog.

Local comparison covered text, multi-turn, function tools, Claude histories, images and OpenAI/Claude input formats. HTTP/TLS evidence and request captures are maintained outside the repository under the workspace artifacts directory.

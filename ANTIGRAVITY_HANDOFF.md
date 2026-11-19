# Antigravity wire2 integration

CPA embeds the Go `agy-wire` engine supplied in `claude-all-in-one-main (1).zip`.
The reference files are under that archive's `sidecars/agy-wire/internal`.
`internal/agywire` preserves the request renderer, protocol writer, model catalog,
monitor configuration, title requests and conversation persistence. Its embedded
adapter accepts CPA's normalized Gemini requests; no CLI subprocess is started.

The outgoing profile is CLI 1.5.11 on Linux/amd64, build 1024248524. Generation
uses HTTP/1.1, chunked transfer, `requestType: agent`, project `aicode-consumers`
and session ID `-3750763034362895579`. The catalog supplies model enums, output
limits and thinking levels. The system prompt includes the supplied CLI template;
client instructions are wrapped in the first user message. Client tools are
exposed as MCP tools, with `toolSummary`, alongside the built-in declarations.

The engine keeps conversation UUIDs, execution IDs and separate step/model-call
counters. It restores saved history, maps tool results back to upstream call IDs,
and reconstructs branched conversations. Sessions are stored in
`auth-dir/.antigravity-wire/<account-hash>/home/`; credentials remain managed by
CPA. Native tool execution is restricted to uploaded/generated files in that
account's directory; other host calls are mapped to declared client tools or denied.

Control requests include model and experiment discovery, user/admin information,
quota summaries, configuration, licenses and title generation. Tool monitoring
uses the supplied model experiment configuration. The former Hub telemetry queue
and its environment switches have been removed. The exact resource-exhausted
429 still returns to CPA's existing prompt-transfer retry handling.

Integration changes: direct function calls replace ACP stdio; CPA owns OAuth,
account routing, proxy selection and API response translation. Connection pools
retain CPA's concurrency settings. Conversation cloning copies append-only slices
instead of serializing the entire history. An idle account engine is released
after one hour; its saved conversations remain available on the next request.

Validation uses local fake upstream responses and local TLS servers. It covers
request structure, a continuing tool conversation, HTTP/1.1 and the CLI user agent.

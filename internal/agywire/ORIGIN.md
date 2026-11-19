# Source

Imported from the user-supplied `claude-all-in-one-main (1).zip`,
`sidecars/agy-wire/internal`, on 2026-11-20.

The native Go engine is hosted in CPA rather than started as a child process.
Import paths and comments were adapted. Added files `agent/embedded.go`,
`agent/client_schema.go` and `agent/builtin.go` implement the CPA input/output
bridge and the reference gateway's client-tool adaptation. CPA supplies the HTTP
client and token provider. The exact resource-exhausted retry remains owned by CPA.

The transport uses native `crypto/tls` and HTTP/1.1 with Go revision
`298f53e483ef15096a05b1d3017cb9d4207cb327`, matching the reference build.
Docker and release jobs bootstrap that revision; manual builds can source
`.github/scripts/setup-antigravity-go.sh` before `go build ./cmd/server`.
Account/proxy connection pools stay inside CPA; no CLI subprocess is required.

`signature/` ports `crates/gw-agy/src/wire_sig.rs`: `agyw1.` envelopes carry the
native thought signature and per-tool Google IDs/summaries, authenticated with
the refresh-token-derived key. Incoming envelopes are verified before protocol
translation. The account engine restores their metadata when rebuilding history.
Claude exposes the envelope in thinking signatures; OpenAI Chat Completions uses
`reasoning_signature` and tool `extra_content.google.thought_signature` fields.

`agent/builtin.go` ports `crates/gw-agy/src/builtin_xlate.rs`. It learns parameter
names once per request, maps read ranges and question options, strips workspace
path prefixes, and falls back from write to edit and from directory/glob queries
to caller-provided shell tools. Unmapped host tools are denied.

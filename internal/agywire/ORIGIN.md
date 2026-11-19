# Source

Imported from the user-supplied `claude-all-in-one-main (1).zip`,
`sidecars/agy-wire/internal`, on 2026-11-20.

The native Go engine is hosted in CPA rather than started as a child process.
Import paths and comments were adapted. Added files `agent/embedded.go`,
`agent/client_schema.go` and `agent/builtin.go` implement the CPA input/output
bridge and the reference gateway's client-tool adaptation. CPA supplies the HTTP
client and token provider. The exact resource-exhausted retry remains owned by CPA.

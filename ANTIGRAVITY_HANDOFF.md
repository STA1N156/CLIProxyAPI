# Antigravity handoff compatibility

The direct Go executor uses the supplied handoff package's Hub 2.31.0 identity,
TLS ClientHello and HTTP/2 negotiation, with HTTP/1.1 fallback. OAuth requests
retain their separate HTTPX user-agent profile. Account and proxy connection
pools remain isolated.

Generation requests use `agent` for tools, `chat` for text and `image_gen` for
images. Agent requests carry the handoff model-enum table and cascade labels;
plain chat/image requests omit agent labels. Agent user text receives the
`USER_REQUEST` and timestamp envelope, except for tool-result messages.

Conversation identity is calculated **before** adding timestamps. This keeps
timestamps from changing the IDs of otherwise identical conversations.
Account identity uses the full SHA-256 of the first available refresh token,
email or access token, matching the handoff. Session IDs are stable per account.
Execution IDs and counters follow
the handoff approximation rather than an official client execution engine.

Auxiliary traffic includes `writeTrajectoryAcls`, `recordTrajectoryAnalytics`,
`recordClientEvent`, `recordCodeAssistMetrics`, `fetchUserInfo`, `listExperiments`
and periodic `fetchAvailableModels`. Telemetry uses the same empty steps/metrics
fields as the handoff. Successful responses enqueue telemetry when the response
body reaches EOF. A shared queue with eight workers bounds background work;
telemetry is skipped if its queue is full instead of blocking generation.

Idle traffic runs once per CPA process and uses the existing account connection
pools. Environment settings:

- `ANTIGRAVITY_PRESENCE_INTERVAL=60`: seconds between sweeps; `0` disables them.
- `ANTIGRAVITY_PRESENCE_CONCURRENCY=8`: maximum parallel account visits (1–64).
- `ANTIGRAVITY_USER_REQUEST_ENVELOPE=1`: set `0` to disable text wrapping.

`USER_PROJECT_DENIED` refreshes the project, updates the request and telemetry
identities, resends the trajectory ACL and retries once. Remaining
`USER_PROJECT_DENIED` and `SUBSCRIPTION_REQUIRED` errors use a one-minute
cooldown. Other 403 behavior and the exact resource-exhausted 429 fallback are
preserved. The package's undefined non-streaming retry variable is not carried
over.

Checks use local mock HTTP/TLS servers and do not consume Google account quota.

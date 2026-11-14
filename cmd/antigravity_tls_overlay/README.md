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

The version updater obtains the version from the official Windows CLI manifest. The build identifier is included only for the measured 1.5.9 release. Future releases require a new capture before adding their build identifier. The previously requested removal of the outer `requestType: "agent"` remains in effect.

The consumer comparison forwarded user information, initialization, model discovery, and one streaming generation request to Google; all returned HTTP 200. The CLI's checkpoint request was answered locally. Other auxiliary requests were inspected locally. The earlier comparison covered the proxy's HTTP and TLS settings with fabricated credentials. Third-party plugin transports are outside this capture.

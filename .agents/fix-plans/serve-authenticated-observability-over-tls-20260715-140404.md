# Fix Plan: Serve authenticated observability endpoints over TLS

## Problem

The broker sink observability HTTP servers (`/metrics`, `/healthz`, `/openapi.json`) and the gRPC observability listener currently require bearer authentication but are served over plaintext HTTP. This exposes topology and operational data to any on-path attacker. The gRPC observability server is started with `ListenAndServe` rather than TLS.

## Proposed Approach

1. Extend `internal/config.Config.ServerTLS` (or add a dedicated inbound TLS block) so sink observability servers can be configured with `cert-file`, `key-file`, and optionally `client-ca-file` for mTLS.
2. Update `internal/cmd/output.go`:
   - `buildSinkServer`: build a TLS listener when a server cert/key is configured; apply the same TLS config to the HTTP server.
   - `buildGRPCServer`: start the observability HTTP server with TLS (`ListenAndServeTLS`) when configured, and ensure the gRPC listener can also use TLS if required.
3. Keep the existing `requireServerTLS` helper pattern used for SSE/gRPC data planes, but allow `--insecure` only in development with the existing loud warning.
4. Ensure bearer auth middleware is still applied on top of TLS.

## Dependencies

- `internal/config/config.go`: add fields for sink-server TLS if not already present; verify `buildServerTLSConfig` is reusable.
- `internal/cmd/output.go`: refactor `buildSinkServer` and `buildGRPCServer` to accept and use the TLS config.
- Tests in `internal/cmd/output*_test.go` and any integration tests that rely on plaintext observability endpoints.

## Risks

- Breaking local/CI setups that rely on plaintext observability endpoints. Mitigation: require explicit `--insecure` for plaintext, matching the existing SSE/gRPC policy.
- Need to avoid conflating inbound server TLS with outbound sink TLS (the codebase already separates these).
- Multi-package change touches config, cmd, and tests; blast radius is moderate-to-high.

## Test Plan

1. Unit tests in `internal/cmd` that verify:
   - plaintext mode is rejected without `--insecure`;
   - valid TLS config starts successfully;
   - mTLS with `client-ca-file` accepts only clients with the right cert.
2. Update integration/e2e tests to pass `--insecure` or provide test TLS certificates.
3. Validate `curl` against `/metrics` over HTTPS returns 401 without token and 200 with token.

## Status

Implementation deferred until approved because this is a multi-package behavioral redesign with deployment-wide implications.

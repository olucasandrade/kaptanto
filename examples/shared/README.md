# Shared Example Utilities

This directory holds small utilities that keep the examples consistent without turning them into a hidden framework.

- `src/cdc-sse.ts`: minimal SSE client for Kaptanto consumers running in Node.
- `src/cdc-types.ts`: shared event types used by the example backends.
- `Dockerfile.kaptanto`: multi-stage build for the main `kaptanto` binary, reused by each example's `docker-compose.yml`.

The examples intentionally keep their product logic local so each app remains understandable in isolation.


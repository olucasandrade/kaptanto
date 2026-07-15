# Releasing

## Go binary

Push a `v*` tag (e.g. `v0.4.0`) to trigger the `release.yml` workflow, which
runs GoReleaser and publishes Docker images.

## npm packages

Package versions are independent of the Go binary version.

### Publish order

`@kaptanto/events` **must** be published before `n8n-nodes-kaptanto` because
the n8n package depends on `@kaptanto/events`. Always tag and wait for the
events package to appear on npm before tagging the n8n package.

1. **`@kaptanto/events`** — push a tag matching `events-v*` (e.g. `events-v0.2.0`).
2. **`n8n-nodes-kaptanto`** — push a tag matching `n8n-v*` (e.g. `n8n-v0.2.0`).

Both tags trigger the `npm-publish.yml` workflow which builds, tests, and
publishes with OIDC provenance (`--provenance --access public`).

You can also publish manually via the `workflow_dispatch` trigger in the
Actions tab.

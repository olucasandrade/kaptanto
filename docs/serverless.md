# Serverless Actions

Invoke AWS Lambda, Cloudflare Workers, and Vercel functions from CDC events using
built-in action types. Every type is an ACT-01 preset: it only produces webhook
sink config — delivery, retries, and cursors stay with the webhook sink.

There is **no pre-warm pinger** and **no Lambda Invoke-API sink**. Function URLs /
HTTPS endpoints are the only path.

## Cold starts

HTTP keep-alive is automatic. The webhook sink reuses a shared `http.Client` with
connection pooling, so successive deliveries to the same host reuse TCP/TLS
connections when the platform allows it. That reduces cold-start overhead for
steady traffic, but it does not keep idle functions warm forever.

Guidance:

| Goal | Approach |
|---|---|
| Fire-and-forget, tolerate cold starts | Use `lambda` with `invocation: async` |
| Latency-critical sync responses | Prefer provisioned concurrency (Lambda) or always-warm plans (Workers/Vercel) |
| High event rate to Workers/Vercel | Enable `batch.max-events` (allowed for `cloudflare-worker` and `vercel`) |
| Exactly-once-ish downstream | Rely on `X-Kaptanto-Idempotency-Key` and make handlers idempotent |

## Action types

### `lambda`

POSTs raw event JSON to an AWS Lambda **Function URL** with SigV4
(`service: lambda`). Credentials come from the standard AWS provider chain
(env, shared config, IMDS/IRSA).

```yaml
actions:
  - name: order-lambda
    type: lambda
    params:
      function-url: ${LAMBDA_FUNCTION_URL}
      region: us-east-1
      invocation: async   # sync (default) | async
    match:
      tables: ["public.orders"]
```

| Param | Required | Secret | Description |
|---|---|---|---|
| `function-url` | Yes | Yes | Lambda Function URL |
| `region` | Yes | No | AWS region for SigV4 |
| `invocation` | No | No | `sync` (default) or `async` |

`batch.max-events` is pinned to **1**. Overrides are rejected at startup.

### `cloudflare-worker`

Plain HTTPS POST to a Worker. Optional static auth header. Batching allowed.

```yaml
actions:
  - name: cf-worker
    type: cloudflare-worker
    params:
      url: ${CF_WORKER_URL}
      auth-header-name: Authorization   # default
      auth-token: ${CF_WORKER_TOKEN}    # optional
    batch:
      max-events: 25
```

| Param | Required | Secret | Description |
|---|---|---|---|
| `url` | Yes | Yes | Worker HTTPS URL |
| `auth-header-name` | No | No | Header name (default `Authorization`) |
| `auth-token` | No | Yes | Static token value for that header |

### `vercel`

Plain HTTPS POST to a Vercel serverless function. Optional Deployment Protection
bypass. Batching allowed.

```yaml
actions:
  - name: vercel-hook
    type: vercel
    params:
      url: ${VERCEL_FN_URL}
      bypass-secret: ${VERCEL_BYPASS}   # optional → x-vercel-protection-bypass
```

| Param | Required | Secret | Description |
|---|---|---|---|
| `url` | Yes | Yes | Function HTTPS URL |
| `bypass-secret` | No | Yes | Sent as `x-vercel-protection-bypass` |

## Response handling

The webhook sink treats any **2xx** status as success. Bodies are read up to
**1 KiB** and logged at debug; the remainder is discarded.

| Mode | Typical status | Meaning |
|---|---|---|
| Lambda `invocation: async` | **202 Accepted** | Event queued; success under the 2xx rule |
| Lambda `invocation: sync` | **200 OK** | Function result body (≤1 KiB snippet at debug) |
| Cloudflare Worker / Vercel | **2xx** | Handler-defined success |

Non-2xx responses follow the usual webhook policy: 408/429/5xx are transient
(router retry); other 4xx are permanent (DLQ when enabled).

## Secrets

Secret params (`function-url`, `url`, `auth-token`, `bypass-secret`) must be
`${ENV_VAR}` references (ACT-02). Literals are rejected at startup and never
appear in OpenAPI discovery.

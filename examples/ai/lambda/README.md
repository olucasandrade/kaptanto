# Lambda + Kaptanto Example

Invoke an AWS Lambda Function URL from CDC events using the built-in `lambda`
action with `invocation: async` and SigV4 auth.

## What It Shows

- A minimal Python handler deployed via **SAM** or **terraform-lite**.
- Kaptanto POSTs raw `ChangeEvent` JSON to the Function URL.
- `invocation: async` sets `X-Amz-Invocation-Type: Event` → Lambda returns **202**
  (success under the webhook 2xx rule).
- Credentials come from the standard AWS provider chain (env, shared config, IMDS/IRSA).

## Architecture

```
Postgres → Kaptanto (action: lambda, async) —SigV4→ Lambda Function URL
```

There is no Lambda Invoke-API sink — Function URLs / HTTPS only (see `docs/serverless.md`).

## Prerequisites

- AWS account + credentials that can create Lambda + Function URLs
- Docker & Docker Compose (optional local Postgres + Kaptanto)
- SAM CLI **or** Terraform ≥ 1.5

## 1. Deploy the function

### Option A — SAM

```bash
cd examples/ai/lambda
sam build
sam deploy --guided
# note the FunctionUrl output
export LAMBDA_FUNCTION_URL='https://xxxx.lambda-url.us-east-1.on.aws/'
export AWS_REGION=us-east-1
```

### Option B — terraform-lite

```bash
cd examples/ai/lambda/terraform
terraform init
terraform apply
export LAMBDA_FUNCTION_URL="$(terraform output -raw function_url)"
export AWS_REGION="$(terraform output -raw region)"
```

## 2. SigV4 / credentials

Kaptanto signs each POST with AWS SigV4 (`service: lambda`). Resolve credentials
the same way the AWS SDK does:

| Source | How |
|--------|-----|
| Environment | `AWS_ACCESS_KEY_ID` + `AWS_SECRET_ACCESS_KEY` (+ optional `AWS_SESSION_TOKEN`) |
| Shared config | `AWS_PROFILE` / `~/.aws/credentials` (compose mounts `$HOME/.aws`) |
| Runtime | IMDS / IRSA when Kaptanto runs on AWS |

The caller identity needs `lambda:InvokeFunctionUrl` (and typically
`lambda:InvokeFunction`) on the target function. Function URL auth is
`AWS_IAM` in both deploy templates.

Startup fails closed if SigV4 is configured but no credentials resolve.

## 3. Run Kaptanto locally against the deployed URL

```bash
cd examples/ai/lambda
export LAMBDA_FUNCTION_URL   # from deploy output
export AWS_REGION=us-east-1
# ensure AWS_ACCESS_KEY_ID/SECRET or a mounted profile work
docker compose up --build -d
```

Trigger a change:

```bash
psql postgres://postgres:postgres@localhost:5443/app -c \
  "UPDATE orders SET status = 'shipped', updated_at = now() WHERE id = 1;"
```

Watch CloudWatch Logs for the function — with `invocation: async` the HTTP
response is **202 Accepted** and the handler runs asynchronously.

## Configuration

```yaml
actions:
  - name: order-lambda
    type: lambda
    params:
      function-url: ${LAMBDA_FUNCTION_URL}  # secret ${VAR} required
      region: ${AWS_REGION}
      invocation: async                     # sync (default) | async
    match:
      tables: ["public.orders"]
```

| Param | Required | Notes |
|-------|----------|-------|
| `function-url` | Yes | Secret `${VAR}` |
| `region` | Yes | SigV4 region |
| `invocation` | No | `async` → `X-Amz-Invocation-Type: Event` |

`batch.max-events` is pinned to **1**. Make the handler idempotent using
`idempotency_key` / `X-Kaptanto-Idempotency-Key`.

## Services (local compose)

| Service | URL |
|---------|-----|
| Postgres | localhost:5443 |
| Kaptanto health | http://localhost:7663/healthz |

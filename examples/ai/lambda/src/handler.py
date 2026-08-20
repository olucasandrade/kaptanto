import json
import logging

logger = logging.getLogger()
logger.setLevel(logging.INFO)


def handler(event, context):
    """Minimal Lambda Function URL handler for Kaptanto CDC events.

    Kaptanto POSTs raw ChangeEvent JSON (or a single-element batch) with
    SigV4 (service=lambda). With invocation: async, Lambda returns 202 and
    this handler runs out-of-band.
    """
    # Function URL may wrap the body; prefer raw string / dict.
    body = event.get("body", event) if isinstance(event, dict) else event
    if isinstance(body, str):
        try:
            payload = json.loads(body)
        except json.JSONDecodeError:
            payload = {"raw": body}
    else:
        payload = body

    idem = None
    if isinstance(payload, dict):
        idem = payload.get("idempotency_key") or (
            (payload.get("headers") or {}).get("x-kaptanto-idempotency-key")
        )
        logger.info(
            "cdc event table=%s op=%s idempotency_key=%s",
            payload.get("table"),
            payload.get("operation"),
            idem,
        )
    else:
        logger.info("cdc payload type=%s", type(payload).__name__)

    # Make handlers idempotent using X-Kaptanto-Idempotency-Key / idempotency_key.
    return {
        "statusCode": 200,
        "headers": {"Content-Type": "application/json"},
        "body": json.dumps({"ok": True, "idempotency_key": idem}),
    }

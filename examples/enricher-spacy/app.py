"""Reference Kaptanto enricher: FastAPI + spaCy (en_core_web_sm).

HTTP contract: POST the full ChangeEvent JSON.
  200 + JSON object → stored as ai_context (≤16 KiB)
  204 → no context
Any other status / timeout / error → Kaptanto fails open (AIC-01).

Stateless: duplicate POSTs after a crash re-send are safe.
"""

from __future__ import annotations

from typing import Any

import spacy
from fastapi import FastAPI, Response
from pydantic import BaseModel, ConfigDict

nlp = spacy.load("en_core_web_sm")
app = FastAPI(title="kaptanto-enricher-spacy")

# Prefer well-known text columns; fall back to any longer string field.
TEXT_KEYS = frozenset(
    {"subject", "body", "title", "description", "message", "content", "text", "notes"}
)


class ChangeEvent(BaseModel):
    model_config = ConfigDict(extra="allow")
    operation: str = ""
    table: str = ""
    after: dict[str, Any] | None = None
    before: dict[str, Any] | None = None


def text_fields(row: dict[str, Any] | None) -> list[tuple[str, str]]:
    if not row:
        return []
    out: list[tuple[str, str]] = []
    for key, val in row.items():
        if key.lower() not in TEXT_KEYS or not isinstance(val, str):
            continue
        s = val.strip()
        if s:
            out.append((key, s))
    return out


def naive_intent(operation: str, table: str) -> str:
    op = (operation or "unknown").lower()
    tbl = (table or "row").lower()
    return f"{op}_on_{tbl}"


@app.get("/healthz")
def healthz() -> dict[str, str]:
    return {"status": "ok"}


@app.post("/enrich", response_model=None)
def enrich(ev: ChangeEvent):
    fields = text_fields(ev.after) or text_fields(ev.before)
    if not fields:
        return Response(status_code=204)

    entities: list[dict[str, str]] = []
    for field, text in fields:
        for ent in nlp(text).ents:
            entities.append({"type": ent.label_, "value": ent.text, "field": field})

    return {
        "intent": naive_intent(ev.operation, ev.table),
        "entities": entities,
    }

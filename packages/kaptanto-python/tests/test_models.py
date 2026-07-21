"""Golden fixture validation for ChangeEvent pydantic models."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
from pydantic import ValidationError

from kaptanto import ChangeEvent, Operation

FIXTURES_PATH = Path(__file__).parent / "fixtures" / "changeevent_fixtures.ndjson"


def load_fixtures() -> list[dict]:
    content = FIXTURES_PATH.read_text(encoding="utf-8")
    return [
        json.loads(line)
        for line in content.splitlines()
        if line.strip()
    ]


@pytest.fixture(scope="module")
def fixtures() -> list[dict]:
    return load_fixtures()


def test_loads_at_least_one_fixture(fixtures: list[dict]) -> None:
    assert len(fixtures) >= 1


def test_every_fixture_line_validates(fixtures: list[dict]) -> None:
    for i, raw in enumerate(fixtures, start=1):
        try:
            ChangeEvent.model_validate(raw)
        except ValidationError as exc:
            pytest.fail(f"fixture line {i} failed validation: {exc}")


def test_fixture_fields_match_go_json_tags(fixtures: list[dict]) -> None:
    required = [
        "id",
        "idempotency_key",
        "timestamp",
        "source",
        "operation",
        "table",
        "key",
        "before",
        "after",
        "metadata",
    ]
    for raw in fixtures:
        for key in required:
            assert key in raw


def test_covers_all_five_operation_types(fixtures: list[dict]) -> None:
    events = [ChangeEvent.model_validate(raw) for raw in fixtures]
    ops = {ev.operation for ev in events}
    expected = {
        Operation.INSERT,
        Operation.UPDATE,
        Operation.DELETE,
        Operation.READ,
        Operation.CONTROL,
    }
    assert expected <= ops


def test_ai_context_optional_and_preserved(fixtures: list[dict]) -> None:
    events = [ChangeEvent.model_validate(raw) for raw in fixtures]
    assert len(events) >= 6

    without = [ev for ev in events if ev.ai_context is None]
    with_ai = [ev for ev in events if ev.ai_context is not None]

    assert len(without) >= 5
    assert len(with_ai) >= 1

    for ev in with_ai:
        assert ev.ai_context is not None
        assert ev.ai_context.intent is not None
        assert isinstance(ev.ai_context.intent, str)


def test_operation_helpers(fixtures: list[dict]) -> None:
    events = [ChangeEvent.model_validate(raw) for raw in fixtures]

    inserts = [ev for ev in events if ev.is_insert()]
    assert inserts
    for ev in inserts:
        assert ev.operation is Operation.INSERT
        assert ev.before is None
        assert ev.after is not None

    updates = [ev for ev in events if ev.is_update()]
    assert updates
    for ev in updates:
        assert ev.before is not None
        assert ev.after is not None

    deletes = [ev for ev in events if ev.is_delete()]
    assert deletes
    for ev in deletes:
        assert ev.before is not None
        assert ev.after is None

    reads = [ev for ev in events if ev.is_read()]
    assert reads
    for ev in reads:
        assert ev.before is None
        assert ev.after is not None

    controls = [ev for ev in events if ev.is_control()]
    assert controls


def test_rejects_invalid_operation() -> None:
    with pytest.raises(ValidationError):
        ChangeEvent.model_validate(
            {
                "id": "x",
                "idempotency_key": "k",
                "timestamp": "t",
                "source": "s",
                "operation": "upsert",
                "table": "t",
                "key": {},
                "before": None,
                "after": {},
                "metadata": {},
            }
        )


def test_rejects_missing_required_fields() -> None:
    with pytest.raises(ValidationError):
        ChangeEvent.model_validate({"id": "x", "operation": "insert"})


def test_extra_fields_allowed() -> None:
    ev = ChangeEvent.model_validate(
        {
            "id": "x",
            "idempotency_key": "k",
            "timestamp": "2026-01-01T00:00:00Z",
            "source": "s",
            "operation": "insert",
            "table": "t",
            "key": {},
            "before": None,
            "after": {"id": 1},
            "metadata": {},
            "future_field": "kept",
        }
    )
    assert ev.model_extra is not None
    assert ev.model_extra.get("future_field") == "kept"

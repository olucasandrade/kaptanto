"""Pydantic models mirroring Go's event.ChangeEvent json tags exactly."""

from __future__ import annotations

from enum import Enum
from typing import Any

from pydantic import BaseModel, ConfigDict


class Operation(str, Enum):
    """Database change operation types, mirroring Go's event.Operation constants."""

    INSERT = "insert"
    UPDATE = "update"
    DELETE = "delete"
    READ = "read"
    CONTROL = "control"


class AIEntity(BaseModel):
    """A single entity extracted by AI enrichment."""

    model_config = ConfigDict(extra="allow")

    type: str
    value: str
    field: str | None = None


class AIEmbedding(BaseModel):
    """Embedding payload attached by AI enrichment."""

    model_config = ConfigDict(extra="allow")

    model: str
    vector: list[float]


class AIContext(BaseModel):
    """Optional AI-generated metadata attached by the enrichment stage.

    Kaptanto treats this as opaque JSON on the wire. The documented shape is
    mirrored here; unknown fields are preserved via ``extra="allow"``.
    """

    model_config = ConfigDict(extra="allow")

    intent: str | None = None
    entities: list[AIEntity] | None = None
    suggested_actions: list[str] | None = None
    embedding: AIEmbedding | None = None
    custom: dict[str, Any] | None = None


class ChangeEvent(BaseModel):
    """Unified change event, mirroring Go's event.ChangeEvent json tags exactly.

    Fields with ``omitempty`` in Go (database, schema, ai_context) are optional.
    ``before`` and ``after`` are always present in the JSON (null when absent).
    ``key`` and ``metadata`` are always present.
    """

    model_config = ConfigDict(extra="allow")

    id: str
    idempotency_key: str
    timestamp: str
    source: str
    operation: Operation
    database: str | None = None
    schema: str | None = None
    table: str
    key: Any
    before: dict[str, Any] | None
    after: dict[str, Any] | None
    metadata: dict[str, Any]
    ai_context: AIContext | None = None

    def is_insert(self) -> bool:
        return self.operation is Operation.INSERT

    def is_update(self) -> bool:
        return self.operation is Operation.UPDATE

    def is_delete(self) -> bool:
        return self.operation is Operation.DELETE

    def is_read(self) -> bool:
        return self.operation is Operation.READ

    def is_control(self) -> bool:
        return self.operation is Operation.CONTROL

/**
 * Database change operation types, mirroring Go's event.Operation constants.
 */
export type Operation = "insert" | "update" | "delete" | "read" | "control";

const VALID_OPERATIONS: ReadonlySet<string> = new Set<Operation>([
  "insert",
  "update",
  "delete",
  "read",
  "control",
]);

/**
 * Unified change event, mirroring Go's event.ChangeEvent json tags exactly.
 *
 * Fields with `omitempty` in Go (database, schema) become optional here.
 * `before` and `after` are always present in the JSON (null when absent).
 * `key` and `metadata` are always present.
 */
export interface ChangeEvent {
  id: string;
  idempotency_key: string;
  timestamp: string;
  source: string;
  operation: Operation;
  database?: string;
  schema?: string;
  table: string;
  key: unknown;
  before: Record<string, unknown> | null;
  after: Record<string, unknown> | null;
  metadata: Record<string, unknown>;
}

function isNonNullObject(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/**
 * Runtime validator: returns true if `obj` structurally matches {@link ChangeEvent}.
 */
export function isChangeEvent(obj: unknown): obj is ChangeEvent {
  if (!isNonNullObject(obj)) return false;

  if (typeof obj.id !== "string" || obj.id === "") return false;
  if (typeof obj.idempotency_key !== "string" || obj.idempotency_key === "")
    return false;
  if (typeof obj.timestamp !== "string" || obj.timestamp === "") return false;
  if (typeof obj.source !== "string" || obj.source === "") return false;
  if (typeof obj.operation !== "string" || !VALID_OPERATIONS.has(obj.operation))
    return false;
  if (typeof obj.table !== "string" || obj.table === "") return false;

  if ("database" in obj && typeof obj.database !== "string") return false;
  if ("schema" in obj && typeof obj.schema !== "string") return false;

  if (!("key" in obj)) return false;
  if (!("before" in obj)) return false;
  if (
    obj.before !== null &&
    !isNonNullObject(obj.before)
  )
    return false;
  if (!("after" in obj)) return false;
  if (
    obj.after !== null &&
    !isNonNullObject(obj.after)
  )
    return false;
  if (!("metadata" in obj) || !isNonNullObject(obj.metadata)) return false;

  return true;
}

/** Narrows to an insert event (before is null, after is populated). */
export function isInsert(ev: ChangeEvent): ev is ChangeEvent & { operation: "insert" } {
  return ev.operation === "insert";
}

/** Narrows to an update event (both before and after are populated). */
export function isUpdate(ev: ChangeEvent): ev is ChangeEvent & { operation: "update" } {
  return ev.operation === "update";
}

/** Narrows to a delete event (before is populated, after is null). */
export function isDelete(ev: ChangeEvent): ev is ChangeEvent & { operation: "delete" } {
  return ev.operation === "delete";
}

/** Narrows to a snapshot read event. */
export function isRead(ev: ChangeEvent): ev is ChangeEvent & { operation: "read" } {
  return ev.operation === "read";
}

/** Narrows to a control/lifecycle event (heartbeat, begin, commit). */
export function isControl(ev: ChangeEvent): ev is ChangeEvent & { operation: "control" } {
  return ev.operation === "control";
}

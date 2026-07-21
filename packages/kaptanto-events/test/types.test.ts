import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import {
  type ChangeEvent,
  type Operation,
  isChangeEvent,
  isControl,
  isDelete,
  isInsert,
  isRead,
  isUpdate,
} from "../src/index.js";

const FIXTURES_PATH = resolve(
  __dirname,
  "fixtures/changeevent_fixtures.ndjson",
);

function loadFixtures(): unknown[] {
  const content = readFileSync(FIXTURES_PATH, "utf-8");
  return content
    .split("\n")
    .filter((line) => line.trim() !== "")
    .map((line) => JSON.parse(line));
}

describe("ChangeEvent fixtures", () => {
  const fixtures = loadFixtures();

  it("loads at least one fixture", () => {
    expect(fixtures.length).toBeGreaterThanOrEqual(1);
  });

  it("every fixture line validates against isChangeEvent", () => {
    for (const [i, fixture] of fixtures.entries()) {
      expect(isChangeEvent(fixture), `fixture line ${i + 1} failed validation`).toBe(true);
    }
  });

  it("fixture fields match expected Go json tags", () => {
    const requiredKeys = [
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
    ];

    for (const fixture of fixtures) {
      const obj = fixture as Record<string, unknown>;
      for (const key of requiredKeys) {
        expect(obj).toHaveProperty(key);
      }
    }
  });

  it("covers all five operation types", () => {
    const ops = new Set(
      fixtures
        .filter(isChangeEvent)
        .map((ev) => ev.operation),
    );
    const expected: Operation[] = ["insert", "update", "delete", "read", "control"];
    for (const op of expected) {
      expect(ops.has(op), `missing operation: ${op}`).toBe(true);
    }
  });

  it("omits ai_context on non-enriched fixtures and preserves it on the enriched line", () => {
    const validated = fixtures.filter(isChangeEvent);
    expect(validated.length).toBeGreaterThanOrEqual(6);

    const without = validated.filter((ev) => ev.ai_context === undefined);
    const withAI = validated.filter((ev) => ev.ai_context !== undefined);

    expect(without.length).toBeGreaterThanOrEqual(5);
    expect(withAI.length).toBeGreaterThanOrEqual(1);

    for (const ev of withAI) {
      expect(ev.ai_context).toEqual(
        expect.objectContaining({
          intent: expect.any(String),
        }),
      );
    }
  });
});

describe("type narrowing helpers", () => {
  const fixtures = loadFixtures().filter(isChangeEvent);

  it("isInsert narrows insert events", () => {
    const inserts = fixtures.filter(isInsert);
    expect(inserts.length).toBeGreaterThanOrEqual(1);
    for (const ev of inserts) {
      expect(ev.operation).toBe("insert");
      expect(ev.before).toBeNull();
      expect(ev.after).not.toBeNull();
    }
  });

  it("isUpdate narrows update events", () => {
    const updates = fixtures.filter(isUpdate);
    expect(updates.length).toBeGreaterThanOrEqual(1);
    for (const ev of updates) {
      expect(ev.operation).toBe("update");
      expect(ev.before).not.toBeNull();
      expect(ev.after).not.toBeNull();
    }
  });

  it("isDelete narrows delete events", () => {
    const deletes = fixtures.filter(isDelete);
    expect(deletes.length).toBeGreaterThanOrEqual(1);
    for (const ev of deletes) {
      expect(ev.operation).toBe("delete");
      expect(ev.before).not.toBeNull();
      expect(ev.after).toBeNull();
    }
  });

  it("isRead narrows read/snapshot events", () => {
    const reads = fixtures.filter(isRead);
    expect(reads.length).toBeGreaterThanOrEqual(1);
    for (const ev of reads) {
      expect(ev.operation).toBe("read");
      expect(ev.before).toBeNull();
      expect(ev.after).not.toBeNull();
    }
  });

  it("isControl narrows control events", () => {
    const controls = fixtures.filter(isControl);
    expect(controls.length).toBeGreaterThanOrEqual(1);
    for (const ev of controls) {
      expect(ev.operation).toBe("control");
    }
  });
});

describe("isChangeEvent rejects invalid inputs", () => {
  it("rejects null", () => expect(isChangeEvent(null)).toBe(false));
  it("rejects undefined", () => expect(isChangeEvent(undefined)).toBe(false));
  it("rejects string", () => expect(isChangeEvent("hello")).toBe(false));
  it("rejects number", () => expect(isChangeEvent(42)).toBe(false));
  it("rejects array", () => expect(isChangeEvent([1, 2])).toBe(false));
  it("rejects empty object", () => expect(isChangeEvent({})).toBe(false));

  it("rejects object with missing required fields", () => {
    expect(
      isChangeEvent({ id: "x", operation: "insert" }),
    ).toBe(false);
  });

  it("rejects object with invalid operation", () => {
    expect(
      isChangeEvent({
        id: "x",
        idempotency_key: "k",
        timestamp: "t",
        source: "s",
        operation: "upsert",
        table: "t",
        key: {},
        before: null,
        after: {},
        metadata: {},
      }),
    ).toBe(false);
  });

  it("rejects object with non-object metadata", () => {
    expect(
      isChangeEvent({
        id: "x",
        idempotency_key: "k",
        timestamp: "t",
        source: "s",
        operation: "insert",
        table: "t",
        key: {},
        before: null,
        after: {},
        metadata: "not-an-object",
      }),
    ).toBe(false);
  });
});

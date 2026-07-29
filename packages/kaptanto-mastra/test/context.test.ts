import { describe, it, expect } from "vitest";
import { toAgentContext, type ChangeEventWithAIContext } from "../src/context.js";
import type { ChangeEvent } from "@kaptanto/events";

function makeEvent(
  overrides: Partial<ChangeEventWithAIContext> = {},
): ChangeEventWithAIContext {
  return {
    id: "01J000000000000000000000AA",
    idempotency_key: "pg:0/1234:1",
    timestamp: "2025-01-01T00:00:00Z",
    source: "postgres",
    operation: "update",
    schema: "public",
    table: "orders",
    key: { id: 1 },
    before: { id: 1, status: "pending" },
    after: { id: 1, status: "shipped" },
    metadata: {},
    ...overrides,
  };
}

describe("toAgentContext", () => {
  it("formats a ChangeEvent without ai_context into compact JSON", () => {
    const ev = makeEvent();
    const ctx = toAgentContext(ev);
    const parsed = JSON.parse(ctx) as Record<string, unknown>;

    expect(parsed).toEqual({
      id: ev.id,
      operation: "update",
      table: "public.orders",
      key: { id: 1 },
      timestamp: ev.timestamp,
      before: { id: 1, status: "pending" },
      after: { id: 1, status: "shipped" },
    });
    expect(parsed).not.toHaveProperty("ai_context");
    expect(parsed).not.toHaveProperty("metadata");
    expect(parsed).not.toHaveProperty("idempotency_key");
  });

  it("includes ai_context when present", () => {
    const ai_context = {
      intent: "fulfill_order",
      suggested_actions: ["notify_customer", "create_shipment"],
    };
    const ev = makeEvent({ ai_context });
    const parsed = JSON.parse(toAgentContext(ev)) as Record<string, unknown>;

    expect(parsed.ai_context).toEqual(ai_context);
    expect(parsed.table).toBe("public.orders");
    expect(parsed.operation).toBe("update");
  });

  it("omits null before/after and uses bare table when schema missing", () => {
    const ev = makeEvent({
      schema: undefined,
      operation: "insert",
      before: null,
      after: { id: 2, status: "new" },
    });
    const parsed = JSON.parse(toAgentContext(ev)) as Record<string, unknown>;

    expect(parsed.table).toBe("orders");
    expect(parsed).not.toHaveProperty("before");
    expect(parsed.after).toEqual({ id: 2, status: "new" });
  });

  it("accepts plain ChangeEvent (no ai_context field)", () => {
    const ev: ChangeEvent = makeEvent();
    delete (ev as ChangeEventWithAIContext).ai_context;
    const ctx = toAgentContext(ev);
    expect(JSON.parse(ctx)).not.toHaveProperty("ai_context");
  });
});

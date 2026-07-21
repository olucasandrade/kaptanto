import type { ChangeEvent } from "@kaptanto/events";

/**
 * ChangeEvent with optional AI enrichment payload (AIC-01).
 * `ai_context` is opaque JSON attached by Kaptanto's enricher stage.
 */
export type ChangeEventWithAIContext = ChangeEvent & {
  ai_context?: unknown;
};

/**
 * Formats a ChangeEvent (+ `ai_context` when present) into a compact JSON
 * string suitable for Mastra agent / workflow context injection.
 */
export function toAgentContext(ev: ChangeEventWithAIContext): string {
  const compact: Record<string, unknown> = {
    id: ev.id,
    operation: ev.operation,
    table: tableRef(ev),
    key: ev.key,
    timestamp: ev.timestamp,
  };

  if (ev.before !== null) compact.before = ev.before;
  if (ev.after !== null) compact.after = ev.after;
  if (ev.ai_context !== undefined && ev.ai_context !== null) {
    compact.ai_context = ev.ai_context;
  }

  return JSON.stringify(compact);
}

function tableRef(ev: ChangeEvent): string {
  if (ev.schema) return `${ev.schema}.${ev.table}`;
  return ev.table;
}

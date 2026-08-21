import { createWorkflow, createStep } from "@mastra/core/workflows";
import { z } from "zod";
import { kaptantoTrigger, toAgentContext } from "@kaptanto/mastra";
import type { ChangeEvent } from "@kaptanto/events";

/**
 * Mastra agent example: react to public.orders CDC events from Kaptanto SSE.
 *
 * Uses a workflow step (no LLM key required) so the compose stack runs
 * offline. Swap the step for an Agent.generate call when you have a model.
 */

const inputSchema = z.object({
  context: z.string(),
  event: z.custom<ChangeEvent>(),
});

const reactToOrder = createStep({
  id: "react-to-order",
  inputSchema,
  outputSchema: z.object({
    summarized: z.string(),
    orderId: z.unknown(),
  }),
  execute: async ({ inputData }) => {
    const { context, event } = inputData;
    const summarized = `CDC ${event.operation} on ${event.schema ?? ""}.${event.table} key=${JSON.stringify(event.key)}`;
    console.log("[mastra] order change:", summarized);
    console.log("[mastra] agent context:", context);
    return { summarized, orderId: event.key };
  },
});

const orderWorkflow = createWorkflow({
  id: "order-change",
  inputSchema,
  outputSchema: z.object({
    summarized: z.string(),
    orderId: z.unknown(),
  }),
})
  .then(reactToOrder)
  .commit();

const url = process.env.KAPTANTO_URL ?? "http://localhost:7654/events";
const consumer = process.env.KAPTANTO_CONSUMER ?? "mastra-orders";

console.log(`[mastra] listening on ${url} as consumer=${consumer}`);
console.log(`[mastra] sample context: ${toAgentContext({
  id: "demo",
  idempotency_key: "demo",
  timestamp: new Date().toISOString(),
  source: "postgres",
  operation: "update",
  schema: "public",
  table: "orders",
  key: { id: 0 },
  before: null,
  after: { id: 0, status: "pending" },
  metadata: {},
})}`);

const handle = kaptantoTrigger({
  url,
  consumer,
  tables: ["public.orders"],
  workflow: orderWorkflow,
  onError: (err, ev) => {
    console.error(`[mastra] workflow failed for ${ev.id}:`, err);
  },
});

const shutdown = () => {
  console.log("[mastra] shutting down");
  handle.close();
};
process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);

await handle.done;
console.log("[mastra] stopped");

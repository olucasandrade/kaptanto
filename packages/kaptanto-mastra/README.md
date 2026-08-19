# @kaptanto/mastra

Mastra adapter for [Kaptanto](https://github.com/olucasandrade/kaptanto) CDC — trigger Mastra workflows from real-time database changes.

## Install

```bash
npm install @kaptanto/mastra @kaptanto/events @mastra/core
```

`@kaptanto/events` and `mastra` are peer dependencies; `@mastra/core` provides the `createWorkflow`/`createStep` API used below.

## Usage

```ts
import { createWorkflow, createStep } from "@mastra/core/workflows";
import { z } from "zod";
import { kaptantoTrigger, toAgentContext } from "@kaptanto/mastra";

const reactToOrder = createStep({
  id: "react-to-order",
  inputSchema: z.object({
    context: z.string(),
    event: z.record(z.unknown()),
  }),
  outputSchema: z.object({ ok: z.boolean() }),
  execute: async ({ inputData }) => {
    console.log("order change:", inputData.context);
    return { ok: true };
  },
});

export const orderWorkflow = createWorkflow({
  id: "order-change",
  inputSchema: z.object({
    context: z.string(),
    event: z.record(z.unknown()),
  }),
  outputSchema: z.object({ ok: z.boolean() }),
})
  .then(reactToOrder)
  .commit();

const handle = kaptantoTrigger({
  url: "http://localhost:7654/events",
  consumer: "mastra-orders",
  tables: ["public.orders"],
  workflow: orderWorkflow,
});

// later
handle.close();
await handle.done;
```

## API

| Export | Description |
|---|---|
| `createKaptantoStream(opts)` | Factory for a `KaptantoStream` SSE client |
| `kaptantoTrigger(opts)` | Consumes SSE and starts Mastra runs via `createRun()` → `start({ inputData })` |
| `toAgentContext(ev)` | Compact JSON string of a ChangeEvent (+ `ai_context` when present) |

Default `inputData` shape: `{ context: toAgentContext(ev), event: ev }`. Override with `mapEvent`.

## License

Apache-2.0

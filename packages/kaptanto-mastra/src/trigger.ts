import type { ChangeEvent } from "@kaptanto/events";
import type { KaptantoStreamOptions } from "@kaptanto/events";
import { createKaptantoStream } from "./stream.js";
import { toAgentContext, type ChangeEventWithAIContext } from "./context.js";

/**
 * Minimal Mastra workflow surface used by {@link kaptantoTrigger}.
 *
 * Pins the current Mastra run API:
 *   `const run = await workflow.createRun(); await run.start({ inputData })`
 *
 * Duck-typed so `@kaptanto/mastra` stays free of a hard `@mastra/core`
 * dependency — callers pass a committed workflow from `createWorkflow(...).commit()`.
 */
export interface MastraWorkflowRun {
  start(opts: { inputData: unknown }): Promise<unknown>;
}

export interface MastraWorkflow {
  createRun(opts?: {
    runId?: string;
    resourceId?: string;
  }): Promise<MastraWorkflowRun> | MastraWorkflowRun;
}

export interface KaptantoTriggerOptions extends KaptantoStreamOptions {
  /** Committed Mastra workflow to start for each CDC event. */
  workflow: MastraWorkflow;
  /**
   * Map a ChangeEvent to workflow `inputData`.
   * Default: `{ context: toAgentContext(ev), event: ev }`.
   */
  mapEvent?: (ev: ChangeEventWithAIContext) => unknown;
  /** Called when a workflow run fails (stream continues). */
  onError?: (err: unknown, ev: ChangeEvent) => void;
  /** AbortSignal to stop the trigger loop. */
  signal?: AbortSignal;
}

export interface KaptantoTriggerHandle {
  /** Stop consuming the SSE stream. */
  close(): void;
  /** Resolves when the trigger loop exits (after close or terminal stream end). */
  done: Promise<void>;
}

function defaultMapEvent(ev: ChangeEventWithAIContext): unknown {
  return {
    context: toAgentContext(ev),
    event: ev,
  };
}

/**
 * Wires a Kaptanto SSE stream into a Mastra workflow trigger.
 *
 * For each ChangeEvent, starts a new Mastra run via:
 * `workflow.createRun()` → `run.start({ inputData })`.
 *
 * @example
 * ```ts
 * const handle = kaptantoTrigger({
 *   url: "http://localhost:7654/events",
 *   consumer: "mastra-orders",
 *   tables: ["public.orders"],
 *   workflow: orderWorkflow,
 * });
 * // later: handle.close(); await handle.done;
 * ```
 */
export function kaptantoTrigger(
  opts: KaptantoTriggerOptions,
): KaptantoTriggerHandle {
  const stream = createKaptantoStream(opts);
  const mapEvent = opts.mapEvent ?? defaultMapEvent;

  let closed = false;
  const close = () => {
    if (closed) return;
    closed = true;
    stream.close();
  };

  if (opts.signal) {
    if (opts.signal.aborted) {
      close();
    } else {
      opts.signal.addEventListener("abort", close, { once: true });
    }
  }

  const done = (async () => {
    try {
      for await (const ev of stream) {
        if (closed) break;
        try {
          const run = await opts.workflow.createRun({
            runId: ev.idempotency_key,
          });
          await run.start({ inputData: mapEvent(ev) });
        } catch (err: unknown) {
          opts.onError?.(err, ev);
        }
      }
    } finally {
      closed = true;
    }
  })();

  return { close, done };
}

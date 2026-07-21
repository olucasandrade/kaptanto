import { task, wait } from "@trigger.dev/sdk/v3";
import type { ChangeEvent } from "@kaptanto/events";

/**
 * A Trigger.dev task that processes order updates from Kaptanto.
 *
 * Kaptanto sends CDC events to Trigger.dev using the `triggerdev` action type.
 * Each event arrives as:
 *   { event: { name: "kaptanto/public.orders.update", payload: ChangeEvent, id: idempotency_key } }
 *
 * The task uses wait.forEvent to coordinate with downstream workflows.
 */
export const processOrderUpdate = task({
  id: "kaptanto-order-update",
  run: async (payload: ChangeEvent) => {
    console.log(
      `Processing ${payload.operation} on ${payload.table}, key: ${payload.key}`
    );

    if (payload.operation === "update" && payload.after?.status === "shipped") {
      const confirmation = await wait.forEvent<{ tracking_number: string }>({
        name: "shipping/tracking-ready",
        filter: { key: ["order_id", payload.key] },
        timeout: "1h",
      });

      if (confirmation) {
        console.log(
          `Tracking ${confirmation.tracking_number} ready for order ${payload.key}`
        );
      }
    }

    return { processed: payload.key, operation: payload.operation };
  },
});

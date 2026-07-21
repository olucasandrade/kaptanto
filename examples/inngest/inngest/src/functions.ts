import { inngest } from "./client";

/**
 * Reacts to order updates captured by Kaptanto.
 *
 * Kaptanto sends each CDC event as an Inngest event with:
 *   name: "kaptanto/public.orders.update"
 *   id:   the idempotency_key from Kaptanto
 *   data: the full ChangeEvent payload
 *
 * Because `id` maps to Kaptanto's idempotency_key, Inngest automatically
 * deduplicates retries — if Kaptanto re-delivers the same event (e.g. after
 * a crash recovery), Inngest will not invoke this function twice.
 */
export const onOrderUpdate = inngest.createFunction(
  { id: "on-order-update", name: "Handle Order Update" },
  { event: "kaptanto/public.orders.update" },
  async ({ event, step }) => {
    const order = event.data;

    await step.run("log-change", async () => {
      console.log(
        `Order ${order.key} updated: status=${order.after?.status}, total=${order.after?.total}`
      );
    });

    if (order.after?.status === "shipped") {
      await step.run("notify-customer", async () => {
        console.log(`Sending shipping notification for order ${order.key}`);
      });
    }

    return { processed: order.key, operation: order.operation };
  }
);

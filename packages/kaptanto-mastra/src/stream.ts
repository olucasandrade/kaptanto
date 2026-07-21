import {
  KaptantoStream,
  type KaptantoStreamOptions,
} from "@kaptanto/events";

/**
 * Factory for a {@link KaptantoStream}. Re-exported as the Mastra-facing
 * entry point so adapters can depend on `@kaptanto/mastra` alone for stream
 * construction while still using the shared `@kaptanto/events` client.
 */
export function createKaptantoStream(
  opts: KaptantoStreamOptions,
): KaptantoStream {
  return new KaptantoStream(opts);
}

export type { KaptantoStreamOptions };
export { KaptantoStream };

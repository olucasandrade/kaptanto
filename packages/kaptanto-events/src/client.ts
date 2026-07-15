import type { ChangeEvent, Operation } from "./types.js";
import { isChangeEvent } from "./types.js";

export interface KaptantoStreamOptions {
  url: string;
  token?: string;
  consumer: string;
  tables?: string[];
  operations?: Operation[];
}

const BASE_DELAY_MS = 1000;
const MAX_DELAY_MS = 30_000;

function backoffDelay(attempt: number): number {
  const exp = Math.min(BASE_DELAY_MS * 2 ** attempt, MAX_DELAY_MS);
  return exp * (0.5 + Math.random() * 0.5);
}

function buildUrl(opts: KaptantoStreamOptions): string {
  const u = new URL(opts.url);
  u.searchParams.set("consumer", opts.consumer);
  if (opts.tables?.length) {
    u.searchParams.set("tables", opts.tables.join(","));
  }
  if (opts.operations?.length) {
    u.searchParams.set("operations", opts.operations.join(","));
  }
  return u.toString();
}

/**
 * Streaming SSE client for Kaptanto CDC events.
 *
 * Usage:
 * ```ts
 * const stream = new KaptantoStream({ url: "http://localhost:7654/events", consumer: "my-svc" });
 * for await (const ev of stream) {
 *   console.log(ev.operation, ev.table, ev.after);
 * }
 * ```
 */
export class KaptantoStream implements AsyncIterable<ChangeEvent> {
  private readonly opts: KaptantoStreamOptions;
  private ctrl = new AbortController();
  private closed = false;

  constructor(opts: KaptantoStreamOptions) {
    this.opts = opts;
  }

  close(): void {
    this.closed = true;
    this.ctrl.abort();
  }

  async *[Symbol.asyncIterator](): AsyncIterableIterator<ChangeEvent> {
    let attempt = 0;

    while (!this.closed) {
      try {
        const headers: Record<string, string> = {
          Accept: "text/event-stream",
        };
        if (this.opts.token) {
          headers["Authorization"] = `Bearer ${this.opts.token}`;
        }

        const res = await fetch(buildUrl(this.opts), {
          headers,
          signal: this.ctrl.signal,
        });

        if (!res.ok) {
          throw new Error(`SSE request failed: ${res.status} ${res.statusText}`);
        }
        if (!res.body) {
          throw new Error("SSE response has no body");
        }

        attempt = 0;
        yield* this.parseStream(res.body);
      } catch (err: unknown) {
        if (this.closed) return;

        const delay = backoffDelay(attempt);
        attempt++;
        await this.sleep(delay);
      }
    }
  }

  private async *parseStream(
    body: ReadableStream<Uint8Array>,
  ): AsyncIterableIterator<ChangeEvent> {
    const decoder = new TextDecoder();
    let buffer = "";

    const reader = body.getReader();
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;

        buffer += decoder.decode(value, { stream: true });

        const frames = buffer.split("\n\n");
        buffer = frames.pop()!;

        for (const frame of frames) {
          const event = this.parseFrame(frame);
          if (event) yield event;
        }
      }

      if (buffer.trim()) {
        const event = this.parseFrame(buffer);
        if (event) yield event;
      }
    } finally {
      reader.releaseLock();
    }
  }

  private parseFrame(raw: string): ChangeEvent | null {
    let dataLines: string[] = [];

    for (const line of raw.split("\n")) {
      if (line === "" || line.startsWith(":")) continue;

      if (line.startsWith("data:")) {
        dataLines.push(line.slice(5).trimStart());
        continue;
      }

      if (line.startsWith("event:") || line.startsWith("id:") || line.startsWith("retry:")) {
        continue;
      }

      console.warn(`KaptantoStream: skipping malformed SSE line: ${line}`);
    }

    if (dataLines.length === 0) return null;

    const payload = dataLines.join("\n");
    let parsed: unknown;
    try {
      parsed = JSON.parse(payload);
    } catch {
      console.warn(`KaptantoStream: skipping non-JSON data: ${payload}`);
      return null;
    }

    if (!isChangeEvent(parsed)) {
      console.warn(`KaptantoStream: skipping invalid ChangeEvent`);
      return null;
    }

    return parsed;
  }

  private sleep(ms: number): Promise<void> {
    return new Promise((resolve, reject) => {
      if (this.closed) {
        resolve();
        return;
      }
      const timer = setTimeout(resolve, ms);
      this.ctrl.signal.addEventListener(
        "abort",
        () => {
          clearTimeout(timer);
          resolve();
        },
        { once: true },
      );
    });
  }
}

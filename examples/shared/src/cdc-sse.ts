import type { KaptantoEvent } from "./cdc-types.js";

type ConsumeOptions = {
  signal?: AbortSignal;
  headers?: Record<string, string>;
};

export async function consumeKaptantoSse(
  url: string,
  onEvent: (event: KaptantoEvent) => void,
  options: ConsumeOptions = {},
): Promise<void> {
  const response = await fetch(url, {
    headers: {
      Accept: "text/event-stream",
      ...options.headers,
    },
    signal: options.signal,
  });

  if (!response.ok || !response.body) {
    throw new Error(`SSE request failed: ${response.status} ${response.statusText}`);
  }

  const decoder = new TextDecoder();
  const reader = response.body.getReader();
  let buffer = "";

  while (true) {
    const { value, done } = await reader.read();
    if (done) {
      break;
    }

    buffer += decoder.decode(value, { stream: true });
    const frames = buffer.split("\n\n");
    buffer = frames.pop() ?? "";

    for (const frame of frames) {
      const dataLines = frame
        .split("\n")
        .filter((line) => line.startsWith("data:"))
        .map((line) => line.slice(5).trim())
        .filter(Boolean);

      if (dataLines.length === 0) {
        continue;
      }

      onEvent(JSON.parse(dataLines.join("\n")) as KaptantoEvent);
    }
  }
}


import { afterEach, describe, expect, it, vi } from "vitest";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { FALLBACK_STARS, loadStarCount } from "../src/routes/api/github-stars";

const landingRoot = path.dirname(fileURLToPath(import.meta.url));

describe("github star chip uses a first-party count", () => {
  it("Nav fetches /api/github-stars, not api.github.com", () => {
    const src = readFileSync(
      path.join(landingRoot, "..", "src", "components", "nav", "Nav.tsx"),
      "utf8",
    );
    expect(src).toContain('fetch("/api/github-stars"');
    expect(src).not.toContain("api.github.com");
    expect(src).toMatch(/^\s+Star$/m);
  });

  it("same-origin endpoint exists and talks to GitHub server-side", () => {
    const src = readFileSync(
      path.join(
        landingRoot,
        "..",
        "src",
        "routes",
        "api",
        "github-stars",
        "index.ts",
      ),
      "utf8",
    );
    expect(src).toContain("onGet");
    expect(src).toContain("AbortController");
    expect(src).toContain(
      "https://api.github.com/repos/olucasandrade/kaptanto",
    );
    expect(src).toContain("kaptanto-landing");
  });
});

describe("loadStarCount", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("returns stargazers_count on success", async () => {
    const fetcher = vi.fn(
      async () =>
        new Response(JSON.stringify({ stargazers_count: 88 }), { status: 200 }),
    ) as unknown as typeof fetch;
    await expect(loadStarCount(fetcher)).resolves.toBe(88);
  });

  it("falls back when the payload is invalid", async () => {
    const fetcher = vi.fn(
      async () =>
        new Response(JSON.stringify({ stargazers_count: "nope" }), {
          status: 200,
        }),
    ) as unknown as typeof fetch;
    await expect(loadStarCount(fetcher)).resolves.toBe(FALLBACK_STARS);
  });

  it("falls back when fetch fails", async () => {
    const fetcher = vi.fn(async () => {
      throw new Error("network");
    }) as unknown as typeof fetch;
    await expect(loadStarCount(fetcher)).resolves.toBe(FALLBACK_STARS);
  });
});

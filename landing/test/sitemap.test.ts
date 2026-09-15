import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { SEO_DOCS } from "../src/data/docs";

// Parity guard for public/sitemap.xml: it must use the canonical domain
// (https://kaptan.to, matching og:url/JSON-LD in src/routes/index.tsx) and
// list exactly one /docs/<slug> URL per SEO_DOCS entry — no missing docs
// routes, no slugs that do not exist. Regenerate the sitemap from SEO_DOCS
// whenever docs are added or removed.

const CANONICAL = "https://kaptan.to";
const sitemap = readFileSync(join(__dirname, "../public/sitemap.xml"), "utf8");
const urls = [...sitemap.matchAll(/<loc>([^<]+)<\/loc>/g)].map((m) => m[1]);
const docUrls = urls.filter((u) => u.startsWith(`${CANONICAL}/docs/`));
const docSlugs = docUrls.map((u) => u.slice(`${CANONICAL}/docs/`.length));

describe("sitemap.xml", () => {
  it("uses the canonical kaptan.to domain for every URL", () => {
    expect(urls.length).toBeGreaterThan(0);
    for (const url of urls) {
      expect(url.startsWith(CANONICAL)).toBe(true);
    }
  });

  it("includes the homepage and docs index", () => {
    expect(urls).toContain(`${CANONICAL}/`);
    expect(urls).toContain(`${CANONICAL}/docs`);
  });

  it("lists every SEO doc slug exactly once", () => {
    expect(new Set(docSlugs).size).toBe(docSlugs.length);
    expect([...docSlugs].sort()).toEqual(SEO_DOCS.map((d) => d.slug).sort());
  });
});

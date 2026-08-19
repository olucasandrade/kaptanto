import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { DOCS_CONTENT } from "../src/data/docs-content";
import { SEO_DOCS } from "../src/data/docs";
import { renderDocPageHtml, rewriteDocLinks } from "../src/data/docs-html";

const landingRoot = path.dirname(fileURLToPath(import.meta.url));

describe("crawlable /docs/[slug] routes render real docs HTML", () => {
  const slugRoute = readFileSync(
    path.join(
      landingRoot,
      "..",
      "src",
      "routes",
      "docs",
      "[slug]",
      "index.tsx",
    ),
    "utf8",
  );

  it("mounts Nav and DocsViewer on the slug route", () => {
    expect(slugRoute).toContain("DocsViewer");
    expect(slugRoute).toMatch(/<Nav /);
    expect(slugRoute).toContain("/legacy.css");
  });

  it("docs-consistency HTML includes Event Log / Badger", () => {
    const html = renderDocPageHtml("docs-consistency");
    expect(html).toBeTruthy();
    expect(html).toContain("Event Log");
    expect(html).toContain("Badger");
  });

  it("rewrites in-doc go() links to crawlable /docs/ hrefs", () => {
    const html = renderDocPageHtml("docs-intro");
    expect(html).toBeTruthy();
    expect(html).toContain('href="/docs/docs-quickstart"');
    expect(html).toContain('href="/docs/docs-schema"');
    expect(html).not.toContain('onclick="go(');
  });

  it("rewrites every go() target in DOCS_CONTENT to a /docs/ href", () => {
    for (const [slug, doc] of Object.entries(DOCS_CONTENT)) {
      if (!doc) continue;
      const html = rewriteDocLinks(doc.body);
      const targets = [...doc.body.matchAll(/go\('([^']+)'\)/g)].map(
        (m) => m[1],
      );
      for (const target of targets) {
        expect(html, `${slug} missing href for ${target}`).toContain(
          `href="/docs/${target}"`,
        );
      }
      expect(html, `${slug} still has SPA-only go()`).not.toContain(
        'onclick="go(',
      );
    }
  });

  it("renderDocPageHtml covers every SEO slug", () => {
    for (const { slug } of SEO_DOCS) {
      const html = renderDocPageHtml(slug);
      expect(html, slug).toBeTruthy();
      expect(html).toContain(DOCS_CONTENT[slug]!.title);
    }
  });
});

import { describe, expect, it } from "vitest";
import { DOCS_CONTENT, SIDEBAR, DOC_FLOW } from "../src/data/docs-content";
import { SEO_DOCS, SEO_DOCS_MAP } from "../src/data/docs";

// Smoke coverage for the two doc registries that must stay in sync:
//   - src/data/docs-content.ts: DOCS_CONTENT (interactive viewer body content),
//     SIDEBAR (nav shown by DocsViewer.tsx), DOC_FLOW (prev/next ordering)
//   - src/data/docs.ts: SEO_DOCS (crawlable /docs/[slug] routes)
// A slug present in the sidebar/flow but missing from DOCS_CONTENT renders
// "Doc not found" in production (see DocsViewer.tsx); a slug missing from
// SEO_DOCS has no crawlable route at /docs/<slug>. These tests exist to catch
// that drift in CI instead of in production.

describe("docs sidebar resolves to real content", () => {
  const sidebarSlugs = SIDEBAR.flatMap((section) =>
    section.items.map(([slug]) => slug),
  );

  it("SIDEBAR is non-empty", () => {
    expect(sidebarSlugs.length).toBeGreaterThan(0);
  });

  it.each(sidebarSlugs)(
    "sidebar slug %s has a DOCS_CONTENT entry",
    (slug) => {
      expect(DOCS_CONTENT[slug]).toBeDefined();
      expect(DOCS_CONTENT[slug].title).toBeTruthy();
      expect(DOCS_CONTENT[slug].body).toBeTruthy();
    },
  );

  it("has no duplicate slugs across sections", () => {
    expect(new Set(sidebarSlugs).size).toBe(sidebarSlugs.length);
  });
});

describe("DOC_FLOW resolves to real content", () => {
  it("DOC_FLOW is non-empty", () => {
    expect(DOC_FLOW.length).toBeGreaterThan(0);
  });

  it.each(DOC_FLOW)("flow slug %s has a DOCS_CONTENT entry", (slug) => {
    expect(DOCS_CONTENT[slug]).toBeDefined();
  });
});

describe("SEO doc index matches the interactive docs registry", () => {
  it("SEO_DOCS is non-empty and slugs are unique", () => {
    expect(SEO_DOCS.length).toBeGreaterThan(0);
    expect(new Set(SEO_DOCS.map((d) => d.slug)).size).toBe(SEO_DOCS.length);
  });

  it.each(SEO_DOCS.map((d) => d.slug))(
    "SEO doc %s also exists in DOCS_CONTENT (interactive view)",
    (slug) => {
      expect(DOCS_CONTENT[slug]).toBeDefined();
    },
  );

  it("SEO_DOCS_MAP looks up every slug", () => {
    for (const doc of SEO_DOCS) {
      expect(SEO_DOCS_MAP.get(doc.slug)).toEqual(doc);
    }
  });
});

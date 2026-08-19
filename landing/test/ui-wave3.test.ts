import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const landingRoot = path.dirname(fileURLToPath(import.meta.url));

describe("landing UI review recovery", () => {
  it("Nav fetches /api/github-stars and labels Star", () => {
    const src = readFileSync(
      path.join(landingRoot, "..", "src", "components", "nav", "Nav.tsx"),
      "utf8",
    );
    expect(src).toContain('fetch("/api/github-stars"');
    expect(src).toMatch(/^\s+Star$/m);
    expect(src).toContain("nscrim");
  });

  it("hero mounts Pipeline3D", () => {
    const src = readFileSync(
      path.join(landingRoot, "..", "src", "components", "hero", "Hero.tsx"),
      "utf8",
    );
    expect(src).toContain("Pipeline3D");
    expect(src).toContain("prefers-reduced-motion");
  });

  it("docs slug route uses og-image and ui-fixes.css", () => {
    const src = readFileSync(
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
    expect(src).toContain("DocsViewer");
    expect(src).toContain("Nav");
    expect(src).toContain("og-image.png");
    expect(src).toContain("/ui-fixes.css");
  });
});

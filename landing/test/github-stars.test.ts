import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

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
    expect(src).toContain(
      "https://api.github.com/repos/olucasandrade/kaptanto",
    );
    expect(src).toContain("kaptanto-landing");
  });
});

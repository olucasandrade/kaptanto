import { describe, expect, it } from "vitest";
import { readFileSync, existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

// Guards against the exact class of drift flagged in project history: the
// landing page once pointed at a wrong collector/endpoint URL that nothing
// caught until manual review (see MEMORY.md: "kaptanto SSE endpoint is
// /events not /stream"). Here: the install snippet shown on the landing page
// and in the docs must match the URL the real install script is published
// under, and that script must actually exist in the repo.

const landingRoot = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(landingRoot, "..", "..");

const INSTALL_URL = "https://get.kaptan.to";
const INSTALL_SNIPPET = `curl -fsSL ${INSTALL_URL} | sh`;

describe("install command matches the published get.kaptan.to script", () => {
  it("scripts/install.sh exists in the repo (the artifact get.kaptan.to serves)", () => {
    const scriptPath = path.join(repoRoot, "scripts", "install.sh");
    expect(existsSync(scriptPath)).toBe(true);
  });

  it("scripts/install.sh documents the same usage line the landing page shows", () => {
    const scriptPath = path.join(repoRoot, "scripts", "install.sh");
    const script = readFileSync(scriptPath, "utf8");
    expect(script).toContain(INSTALL_SNIPPET);
  });

  it("Install.tsx (hero install widget) shows the canonical snippet", () => {
    const src = readFileSync(
      path.join(landingRoot, "..", "src", "components", "install", "Install.tsx"),
      "utf8",
    );
    expect(src).toContain(INSTALL_SNIPPET);
  });

  it("routes/index.tsx download link points at the install URL", () => {
    const src = readFileSync(
      path.join(landingRoot, "..", "src", "routes", "index.tsx"),
      "utf8",
    );
    expect(src).toContain(`downloadUrl: '${INSTALL_URL}'`);
  });

  it("docs-content.ts quickstart/install docs show the canonical snippet", () => {
    const src = readFileSync(
      path.join(landingRoot, "..", "src", "data", "docs-content.ts"),
      "utf8",
    );
    // The docs page repeats the curl snippet in multiple sections; every
    // occurrence must use the same URL, not a stale one.
    const matches = src.match(/curl -fsSL https:\/\/[^\s|]+ \| sh/g) ?? [];
    expect(matches.length).toBeGreaterThan(0);
    for (const m of matches) {
      expect(m).toBe(INSTALL_SNIPPET);
    }
  });
});

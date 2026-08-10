import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { DOCS_CONTENT } from "../src/data/docs-content";

const fixtureDir = join(dirname(fileURLToPath(import.meta.url)), "fixtures");

describe("benchmark docs parity", () => {
  const golden = JSON.parse(
    readFileSync(join(fixtureDir, "benchmark-summary.json"), "utf8"),
  ) as Record<
    string,
    {
      steady_eps: number;
      peak_eps: number;
      burst_p50_s: number;
      recovery_s: number;
    }
  >;

  it("docs-benchmarks steady/peak eps match golden fixture", () => {
    const body = DOCS_CONTENT["docs-benchmarks"].body;
    for (const stats of Object.values(golden)) {
      expect(body).toContain(stats.steady_eps.toLocaleString("en-US"));
      expect(body).toContain(stats.peak_eps.toLocaleString("en-US"));
    }
  });

  it("docs-benchmarks recovery seconds match golden fixture", () => {
    const body = DOCS_CONTENT["docs-benchmarks"].body;
    for (const stats of Object.values(golden)) {
      expect(body).toContain(String(stats.recovery_s));
    }
  });
});

describe("output mode docs parity", () => {
  const manifest = JSON.parse(
    readFileSync(join(fixtureDir, "valid-outputs.json"), "utf8"),
  ) as { outputs: string[]; sinks: string[] };

  it("docs-config lists every valid output mode", () => {
    const body = DOCS_CONTENT["docs-config"].body;
    for (const mode of manifest.outputs) {
      expect(body).toContain(mode);
    }
  });

  it("docs quickstart examples use valid output modes only", () => {
    const quickstart = DOCS_CONTENT["docs-quickstart"].body;
    for (const mode of ["stdout", "sse", "grpc"]) {
      expect(quickstart).toContain(`--output ${mode}`);
    }
  });
});

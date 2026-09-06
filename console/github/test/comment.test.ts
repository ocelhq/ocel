import { expect, test } from "vitest";
import { MARKER, type PreviewInput, renderComment } from "../src/preview";
import type { DeployResult } from "../src/report";

const SHA = "0123456789abcdef0123456789abcdef01234567";

const result: DeployResult = {
  schemaVersion: 1,
  slug: "ocelhq",
  environment: { class: "preview", identity: "pr-7" },
  promotionId: "prom_1",
  apps: [
    { name: "web", urls: ["https://web.preview.example"] },
    { name: "worker", urls: [] },
  ],
  deployedAt: "2026-09-06T10:11:12Z",
};

const base: PreviewInput = {
  repo: "ocelhq/ocel",
  pr: 7,
  sha: SHA,
  ref: "feature/preview",
  runUrl: "https://github.com/ocelhq/ocel/actions/runs/42",
  phase: "started",
};

test("started announces the deploy and links the run", () => {
  const body = renderComment("started", base);

  expect(body.startsWith(MARKER)).toBe(true);
  expect(body).toContain("Deploying…");
  expect(body).toContain(`[\`0123456\`](https://github.com/ocelhq/ocel/commit/${SHA})`);
  expect(body).toContain("[Run log](https://github.com/ocelhq/ocel/actions/runs/42)");
});

test("deployed renders one table row per app", () => {
  const body = renderComment("deployed", { ...base, phase: "deployed", result });

  expect(body.startsWith(MARKER)).toBe(true);
  expect(body).toContain("| App | URL | Commit | Updated |");
  expect(body).toContain("| web | https://web.preview.example |");
  expect(body).toContain("| worker | no url |");
  expect(body).toContain("2026-09-06T10:11:12Z");
  expect(body.split("\n").filter((line) => line.startsWith("| "))).toHaveLength(4);
});

test("failed reports the error and the run", () => {
  const body = renderComment("failed", { ...base, phase: "failed", error: "build exited 1" });

  expect(body.startsWith(MARKER)).toBe(true);
  expect(body).toContain("Preview failed");
  expect(body).toContain("build exited 1");
  expect(body).toContain("[Run log](https://github.com/ocelhq/ocel/actions/runs/42)");
});

test("removed says the preview is gone and needs no run", () => {
  const body = renderComment("removed", { ...base, phase: "removed", runUrl: undefined });

  expect(body.startsWith(MARKER)).toBe(true);
  expect(body).toContain("torn down");
  expect(body).not.toContain("Run log");
});

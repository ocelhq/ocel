import { expect, test } from "vitest";
import { formatTime, MARKER, type PreviewInput, renderComment } from "../src/preview";
import type { DeployResult } from "../src/report";

const SHA = "0123456789abcdef0123456789abcdef01234567";

const result: DeployResult = {
  schemaVersion: 1,
  slug: "ocelhq",
  environment: { class: "preview", identity: "pr-7" },
  provider: { package: "@ocel/provider-aws", region: "eu-west-2" },
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
  expect(body).toContain("🔄 Deploying");
  expect(body).toContain(`[\`0123456\`](https://github.com/ocelhq/ocel/commit/${SHA})`);
  expect(body).toContain("[Run log](https://github.com/ocelhq/ocel/actions/runs/42)");
});

test("deployed leads with the provider mark and lands each app beside its checkmark", () => {
  const body = renderComment("deployed", { ...base, phase: "deployed", result });

  expect(body.startsWith(MARKER)).toBe(true);
  expect(body).toContain(
    '<img src="https://raw.githubusercontent.com/ocelhq/ocel/main/www/public/providers/aws.svg" alt="AWS" height="14"> Deployed [`0123456`]',
  );
  expect(body).toContain("to your AWS account · eu-west-2 · Sep 6, 2026, 10:11 UTC");
  expect(body).toContain("| App | Preview | Status |");
  expect(body).toContain("| web | [Visit preview ↗](https://web.preview.example) | ✅ Ready |");
  expect(body).toContain("| worker | no url | ❌ No URL |");
  expect(body).not.toContain("2026-09-06T10:11:12Z");
  expect(body.split("\n").filter((line) => line.startsWith("| "))).toHaveLength(4);
});

test("a provider without a mark is named by its package and gets no image", () => {
  const body = renderComment("deployed", {
    ...base,
    phase: "deployed",
    result: { ...result, provider: { package: "@ocel/provider-moon" } },
  });

  expect(body).toContain("Deployed [`0123456`]");
  expect(body).toContain("to @ocel/provider-moon · Sep 6, 2026, 10:11 UTC");
  expect(body).not.toContain("<img");
});

test("a time that does not parse is shown as it came", () => {
  expect(formatTime("soon")).toBe("soon");
});

test("failed reports the error and the run", () => {
  const body = renderComment("failed", { ...base, phase: "failed", error: "build exited 1" });

  expect(body.startsWith(MARKER)).toBe(true);
  expect(body).toContain("❌ Preview failed");
  expect(body).toContain("build exited 1");
  expect(body).toContain("[Run log](https://github.com/ocelhq/ocel/actions/runs/42)");
});

test("removed says the preview is gone and needs no run", () => {
  const body = renderComment("removed", { ...base, phase: "removed", runUrl: undefined });

  expect(body.startsWith(MARKER)).toBe(true);
  expect(body).toContain("💤 Preview torn down");
  expect(body).not.toContain("Run log");
});

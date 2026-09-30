import { readFileSync } from "node:fs";
import { expect, test } from "vitest";
import { MARKER } from "../src/preview";
import { renderReport } from "../src/render";

const report = {
  repo: "ocelhq/ocel",
  pr: 7,
  sha: "0123456789abcdef0123456789abcdef01234567",
  ref: "feature/preview",
  run_url: "https://github.com/ocelhq/ocel/actions/runs/42",
  phase: "deployed",
  result: {
    slug: "ocelhq",
    environment: { tier: "preview", identity: "pr-7" },
    provider: { name: "aws" },
    promotionId: "prom_1",
    apps: [{ name: "web", urls: ["https://web.preview.example"] }],
    deployedAt: "2026-09-06T10:11:12Z",
  },
};

test("a deployed report renders the comment the app would post", () => {
  const body = renderReport(report);

  expect(body.startsWith(MARKER)).toBe(true);
  expect(body).toContain(
    '<img src="https://raw.githubusercontent.com/ocelhq/ocel/main/www/public/providers/aws.svg" alt="AWS" height="14">',
  );
  expect(body).toContain("| web | [Visit preview ↗](https://web.preview.example) | ✅ Ready |");
  expect(body).toContain("[Run log](https://github.com/ocelhq/ocel/actions/runs/42)");
});

test("a deployed report carrying the result the CLI writes renders", () => {
  const written = JSON.parse(
    readFileSync(
      new URL("../../../cli/internal/deployrecord/testdata/deploy-result.json", import.meta.url),
      "utf8",
    ),
  );

  const body = renderReport({ ...report, result: written });

  expect(body).toContain("| web | [Visit preview ↗](https://app.example.com) | ✅ Ready |");
});

test("a report that is not one is refused", () => {
  expect(() => renderReport({ ...report, sha: "nope" })).toThrow();
});

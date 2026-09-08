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
    schemaVersion: 1,
    slug: "ocelhq",
    environment: { class: "preview", identity: "pr-7" },
    provider: { package: "@ocel/provider-aws", region: "eu-west-2" },
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

test("a report that is not one is refused", () => {
  expect(() => renderReport({ ...report, sha: "nope" })).toThrow();
});

import { createHmac } from "node:crypto";
import type { AddressInfo } from "node:net";
import { afterAll, beforeAll, expect, test, vi } from "vitest";
import { createApp } from "../src/http";
import type { Report } from "../src/report";

const SECRET = "report-secret";

const report = {
  repo: "ocelhq/ocel",
  pr: 7,
  sha: "a".repeat(40),
  ref: "feature/preview",
  run_url: "https://github.com/ocelhq/ocel/actions/runs/1",
  phase: "started",
};

const onReport = vi.fn<(report: Report) => Promise<void>>(async () => {});

let origin: string;
let server: ReturnType<ReturnType<typeof createApp>["listen"]>;

beforeAll(async () => {
  const app = createApp({ reportSecret: SECRET, onReport });
  await new Promise<void>((resolve) => {
    server = app.listen(0, () => {
      resolve();
    });
  });
  const { port } = server.address() as AddressInfo;
  origin = `http://127.0.0.1:${port}`;
});

afterAll(async () => {
  await new Promise<void>((resolve, reject) => {
    server.close((error) => (error ? reject(error) : resolve()));
  });
});

function sign(body: string): string {
  return `sha256=${createHmac("sha256", SECRET).update(body).digest("hex")}`;
}

function post(body: string, headers: Record<string, string>): Promise<Response> {
  return fetch(`${origin}/api/preview`, {
    method: "POST",
    headers: { "content-type": "application/json", ...headers },
    body,
  });
}

test("healthz answers ok", async () => {
  const response = await fetch(`${origin}/healthz`);
  expect(response.status).toBe(200);
  await expect(response.json()).resolves.toEqual({ ok: true });
});

test("a valid signature is accepted", async () => {
  onReport.mockClear();
  const body = JSON.stringify(report);
  const response = await post(body, { "x-ocel-signature-256": sign(body) });

  expect(response.status).toBe(200);
  await expect(response.json()).resolves.toEqual({ ok: true });
  expect(onReport).toHaveBeenCalledTimes(1);
  expect(onReport.mock.calls[0]?.[0].pr).toBe(7);
});

test("a tampered body is rejected", async () => {
  onReport.mockClear();
  const body = JSON.stringify(report);
  const response = await post(JSON.stringify({ ...report, pr: 8 }), {
    "x-ocel-signature-256": sign(body),
  });

  expect(response.status).toBe(401);
  expect(onReport).not.toHaveBeenCalled();
});

test("a missing signature is rejected", async () => {
  onReport.mockClear();
  const response = await post(JSON.stringify(report), {});

  expect(response.status).toBe(401);
  expect(onReport).not.toHaveBeenCalled();
});

test("a signed body that fails the schema is rejected", async () => {
  onReport.mockClear();
  const body = JSON.stringify({ ...report, phase: "deployed" });
  const response = await post(body, { "x-ocel-signature-256": sign(body) });

  expect(response.status).toBe(400);
  expect(onReport).not.toHaveBeenCalled();
});

test("a github failure surfaces as 502", async () => {
  onReport.mockClear();
  onReport.mockRejectedValueOnce(new Error("boom"));
  const body = JSON.stringify(report);
  const response = await post(body, { "x-ocel-signature-256": sign(body) });

  expect(response.status).toBe(502);
});

import { createServer } from "node:http";
import { randomUUID } from "node:crypto";

export const needsSources = false;

const WARM_MS = Number(process.env.BENCH_FAKE_WARM_MS ?? 2);
const COLD_MS = Number(process.env.BENCH_FAKE_COLD_MS ?? 14);
const INIT_MS = Number(process.env.BENCH_FAKE_INIT_MS ?? 9);
const JITTER = Number(process.env.BENCH_FAKE_JITTER ?? 0.25);
const BUILD_MS = Number(process.env.BENCH_FAKE_BUILD_MS ?? 4_000);
const PROVISION_MS = Number(process.env.BENCH_FAKE_PROVISION_MS ?? 11_000);
const MEMORY_MB = 1024;

const running = new Map();

export async function deploy({ app, platform, region, log }) {
  const functionName = `fake-${app.name}-${platform.id}-${region}`;
  const state = { app, reports: [], cold: false, server: null, port: 0 };

  state.server = createServer(async (req, res) => {
    const cold = state.cold;
    state.cold = false;
    const initDurationMs = cold ? jitter(INIT_MS) : null;
    const durationMs = jitter(cold ? COLD_MS : WARM_MS);
    await sleep((initDurationMs ?? 0) + durationMs);
    if (req.url?.split("?")[0] !== "/health") {
      res.writeHead(404).end("no such route on the fake platform");
      return;
    }
    state.reports.push({
      timeMs: Date.now(),
      line: reportLine({ durationMs, initDurationMs }),
    });
    res.writeHead(200, { "content-type": "application/json" });
    res.end(JSON.stringify({ ok: true, framework: app.framework, platform: platform.id }));
  });

  await new Promise((ok, fail) => {
    state.server.once("error", fail);
    state.server.listen(0, "127.0.0.1", ok);
  });
  state.port = state.server.address().port;
  running.set(functionName, state);
  log?.(`fake platform listening on 127.0.0.1:${state.port}, nothing was deployed`);

  return {
    url: `http://127.0.0.1:${state.port}`,
    functionName,
    buildMs: jitter(BUILD_MS),
    provisionMs: jitter(PROVISION_MS),
  };
}

export async function teardown({ deployment, log }) {
  const state = running.get(deployment?.functionName);
  if (!state) return;
  running.delete(deployment.functionName);
  await new Promise((ok) => state.server.close(ok));
  log?.(`fake platform stopped after ${state.reports.length} invocation(s)`);
}

export const measurementOps = Object.freeze({
  logSettleMs: 0,
  async forceColdStart({ functionName }) {
    const state = running.get(functionName);
    if (!state) throw new Error(`${functionName} is not a running fake deployment`);
    state.cold = true;
  },
  async fetchReportLines({ functionName, startMs, endMs }) {
    const state = running.get(functionName);
    if (!state) throw new Error(`${functionName} is not a running fake deployment`);
    return state.reports
      .filter((report) => report.timeMs >= startMs && report.timeMs <= endMs)
      .map((report) => report.line);
  },
});

function reportLine({ durationMs, initDurationMs }) {
  return [
    `REPORT RequestId: ${randomUUID()}`,
    `Duration: ${durationMs.toFixed(2)} ms`,
    `Billed Duration: ${Math.ceil(durationMs + (initDurationMs ?? 0))} ms`,
    `Memory Size: ${MEMORY_MB} MB`,
    `Max Memory Used: ${Math.round(MEMORY_MB / 12)} MB`,
    ...(initDurationMs === null ? [] : [`Init Duration: ${initDurationMs.toFixed(2)} ms`]),
  ].join("\t");
}

function jitter(ms) {
  return ms * (1 + (Math.random() * 2 - 1) * JITTER);
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

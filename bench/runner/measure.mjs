import { PROBE_ROUTE } from "../matrix.config.mjs";
import { forceColdStart, sleep } from "./aws.mjs";
import { fetchReportLines, parseReportLines, reportCountProblem, splitByInit } from "./logs.mjs";

export const REQUEST_TIMEOUT_MS = 30_000;

export const LOG_WINDOW_PAD_MS = 60_000;

export const defaultOps = Object.freeze({ forceColdStart, fetchReportLines });

export async function measure({ app, deployment, region, samples, ops = defaultOps, log = () => {}, aborted = () => false }) {
  const target = new URL(PROBE_ROUTE, deployment.url).toString();
  const errors = [];
  const warnings = [];
  const startedAt = Date.now();

  const cold = [];
  for (let i = 0; i < samples.cold && !aborted(); i += 1) {
    await ops.forceColdStart({ functionName: deployment.functionName, region, nonce: `${startedAt}-${i}` });
    const sample = await probe(target, app);
    if (sample.error) {
      errors.push(`cold sample ${i + 1}: ${sample.error}`);
    } else {
      cold.push(sample);
    }
    log(`cold ${cold.length}/${samples.cold}${sample.error ? " (invalid)" : ` ${sample.rttMs.toFixed(1)} ms`}`);
  }

  for (let i = 0; i < samples.warmup && !aborted(); i += 1) {
    await probe(target, app);
  }

  const warm = [];
  for (let i = 0; i < samples.warm && !aborted(); i += 1) {
    const sample = await probe(target, app);
    if (sample.error) {
      errors.push(`warm sample ${i + 1}: ${sample.error}`);
    } else {
      warm.push(sample);
    }
  }
  log(`warm ${warm.length}/${samples.warm} valid, ${samples.warmup} warm-up request(s) discarded`);

  const finishedAt = Date.now();

  const settleMs = ops.logSettleMs ?? samples.logSettleMs;
  if (settleMs > 0) {
    log(`waiting ${Math.round(settleMs / 1000)}s for CloudWatch to ingest the REPORT lines`);
    await sleep(settleMs);
  }

  let reports = [];
  try {
    const lines = await ops.fetchReportLines({
      functionName: deployment.functionName,
      logGroupName: deployment.logGroupName,
      region,
      startMs: startedAt - LOG_WINDOW_PAD_MS,
      endMs: finishedAt + LOG_WINDOW_PAD_MS,
    });
    reports = parseReportLines(lines);
  } catch (err) {
    warnings.push(`no REPORT lines were read, so no Init Duration or billed Duration is known: ${err.message}`);
  }

  const split = splitByInit(reports);
  const mismatch = reportCountProblem({ coldReports: split.cold.length, coldSamples: samples.cold });
  if (mismatch && reports.length > 0) {
    warnings.push(mismatch);
  }

  return {
    startedAt,
    finishedAt,
    window: { startMs: startedAt - LOG_WINDOW_PAD_MS, endMs: finishedAt + LOG_WINDOW_PAD_MS },
    cold: { samples: cold, reports: split.cold },
    warm: { samples: warm, reports: split.warm },
    errors,
    warnings,
  };
}

async function probe(target, app) {
  const startedAt = Date.now();
  const start = performance.now();
  try {
    const response = await fetch(target, { signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS) });
    const body = await response.text();
    const rttMs = performance.now() - start;
    const finishedAt = Date.now();
    const invalid = probeProblem(app, response.status, body);
    if (invalid) return { startedAt, finishedAt, rttMs: null, error: invalid };
    return { startedAt, finishedAt, rttMs, status: response.status };
  } catch (err) {
    return { startedAt, finishedAt: Date.now(), rttMs: null, error: `${target} did not answer: ${err.message}` };
  }
}

export function probeProblem(app, status, body) {
  if (status !== 200) {
    return `${status}, not 200: ${String(body).slice(0, 200)}`;
  }
  let parsed;
  try {
    parsed = JSON.parse(body);
  } catch {
    return `200 with a body that is not JSON, so it did not come from ${app.name}'s ${PROBE_ROUTE}: ${String(body).slice(0, 200)}`;
  }
  if (parsed?.ok !== true) {
    return `200 with ${JSON.stringify(parsed).slice(0, 200)}, which is not ${app.name}'s ${PROBE_ROUTE} body`;
  }
  const named = parsed.framework ?? parsed.name;
  if (named !== undefined && named !== app.framework && named !== app.name) {
    return `200 naming ${JSON.stringify(named)}, but this cell deployed ${app.name} (${app.framework}); the URL points at the wrong app`;
  }
  return null;
}

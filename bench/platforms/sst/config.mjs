import { createHash } from "node:crypto";
import { join } from "node:path";

export const SST_VERSION = "4.17.1";

export const CONFIG_FILE = "sst.config.ts";

export const TOOLCHAIN_DIR = ".bench-sst-toolchain";

export const STATE_FILE = ".bench-sst.json";

export const OUTPUTS_FILE = join(".sst", "outputs.json");

export const COMPONENT = "Bench";

export const HANDLER = "src/handler.handler";

export const TIMEOUT_SECONDS = 30;

export const APP_PREFIX = "bench";

export const RUN_ID_ENV = "BENCH_RUN_ID";

export const BOOTSTRAP_PARAMETER = "/sst/bootstrap";

export const PASSPHRASE_PREFIX = "/sst/passphrase";

export const TOKEN_LEN = 8;

function sanitize(value) {
  return String(value ?? "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

function hash(value) {
  return createHash("sha256").update(String(value ?? "")).digest("hex").slice(0, TOKEN_LEN);
}

export function sstAppName(app) {
  return `${APP_PREFIX}-${sanitize(app) || "app"}`;
}

export function sstStage({ workdir, runId }) {
  const run = sanitize(runId).slice(0, TOKEN_LEN).replace(/-+$/, "");
  const place = hash(String(workdir ?? "").replace(/\/+$/, ""));
  return `s${run ? `${run}-${place}` : place}`;
}

export function passphraseParameter(appName, stage) {
  return `${PASSPHRASE_PREFIX}/${appName}/${stage}`;
}

export function renderSstConfig({ appName, region, pinned, timeoutSeconds = TIMEOUT_SECONDS }) {
  return [
    `/// <reference path="./.sst/platform/config.d.ts" />`,
    ``,
    `export default $config({`,
    `  app() {`,
    `    return {`,
    `      name: ${JSON.stringify(appName)},`,
    `      home: "aws",`,
    `      removal: "remove",`,
    `      providers: { aws: { region: ${JSON.stringify(region)} } },`,
    `    };`,
    `  },`,
    `  async run() {`,
    `    const fn = new sst.aws.Function(${JSON.stringify(COMPONENT)}, {`,
    `      handler: ${JSON.stringify(HANDLER)},`,
    `      runtime: ${JSON.stringify(pinned.runtime)},`,
    `      memory: ${JSON.stringify(`${pinned.memoryMB} MB`)},`,
    `      architecture: ${JSON.stringify(pinned.architecture)},`,
    `      timeout: ${JSON.stringify(`${timeoutSeconds} seconds`)},`,
    `      url: { authorization: "none" },`,
    `    });`,
    `    return { functionName: fn.name, url: fn.url };`,
    `  },`,
    `});`,
    ``,
  ].join("\n");
}

export function outputsProblems(outputs) {
  if (!outputs || typeof outputs !== "object") {
    return [`${OUTPUTS_FILE} held ${JSON.stringify(outputs)}, not the object run() returns`];
  }
  const problems = [];
  if (typeof outputs.functionName !== "string" || !outputs.functionName) {
    problems.push(
      `${OUTPUTS_FILE} has no functionName; without the deployed Lambda's name no cold start can be forced`,
    );
  }
  if (typeof outputs.url !== "string" || !/^https?:\/\//.test(outputs.url ?? "")) {
    problems.push(`${OUTPUTS_FILE} has url ${JSON.stringify(outputs.url)}, not an http(s) URL`);
  }
  return problems;
}

export function pinProblems(configuration, pinned) {
  const problems = [];
  const runtime = configuration?.Runtime;
  const memory = configuration?.MemorySize;
  const architecture = (configuration?.Architectures ?? [])[0];
  if (runtime !== pinned?.runtime) {
    problems.push(`runtime is ${JSON.stringify(runtime)}, not the pinned ${JSON.stringify(pinned?.runtime)}`);
  }
  if (Number(memory) !== Number(pinned?.memoryMB)) {
    problems.push(`memory is ${JSON.stringify(memory)} MB, not the pinned ${JSON.stringify(pinned?.memoryMB)} MB`);
  }
  if (architecture !== pinned?.architecture) {
    problems.push(
      `architecture is ${JSON.stringify(architecture)}, not the pinned ${JSON.stringify(pinned?.architecture)}`,
    );
  }
  return problems;
}

export function reclaimMessage({ appName, stage, region, workdir, problems }) {
  return [
    `[bench/sst] TEARDOWN FAILED for app ${appName} stage ${stage} in ${region}: ${problems.join("; ")}`,
    `[bench/sst] its Lambda, role, log group and function URL are still live and still billable; reclaim them by running:`,
    `  cd ${workdir} && ${join(TOOLCHAIN_DIR, "node_modules", ".bin", "sst")} remove --stage ${stage}`,
    `  aws ssm delete-parameter --region ${region} --name ${passphraseParameter(appName, stage)}`,
    `[bench/sst] SST's account-wide bootstrap (the sst-asset-* and sst-state-* buckets, the sst-asset ECR repo,`,
    `[bench/sst] ${BOOTSTRAP_PARAMETER} and the Live AppSync Events API) is shared by every stage and outlives`,
    `[bench/sst] every \`sst remove\`; take it with removeBootstrap() once the whole matrix has finished.`,
  ].join("\n");
}

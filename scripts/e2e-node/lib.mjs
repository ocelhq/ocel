// Pure helpers for the plain-node deployment-e2e harness (deploy.mjs,
// cleanup.mjs, assert-bytecode.mjs, assert-embed.mjs).
//
// Everything framework-agnostic — project/slug derivation, the ocel.config.ts
// renderer, the bytecode-cache key shape and its CloudWatch line matching, the
// tar/zip readers — lives in @ocel-scripts/e2e-shared and is re-exported below.
// Only what is specific to this harness's own smoke app (its routes and
// response markers, and how its project slug is derived without an external
// test harness driving it) is defined here.

import { join } from "node:path";

import { projectSlug, renderOcelConfig as renderSharedOcelConfig } from "@ocel-scripts/e2e-shared/lib.mjs";

export * from "@ocel-scripts/e2e-shared/lib.mjs";

/** The file deploy.mjs persists this app's identity to, read by cleanup.mjs. */
export const STATE_FILE = ".ocel-e2e.json";

/** The file every byte of the deploy's output is redirected to. */
export const BUILD_LOG_FILE = ".adapter-build.log";

/** The CLI's machine-readable deploy result, relative to the app directory. */
export const DEPLOY_RESULT_FILE = join(".ocel", "deploy-result.json");

/**
 * The name the smoke app is declared under. Isolation lives in the project
 * slug (projectSlug), so a per-app name would buy nothing — see
 * scripts/e2e-next/lib.mjs's APP_NAME for the same reasoning.
 */
export const APP_NAME = "app";

/**
 * The framework string this harness's ocel.config.ts declares. A real,
 * recognized value the deploy pipeline switches its build strategy on
 * (cli/platform/src/builder/registry.ts) — not a label; see bytecode.go's
 * isNodeRuntime for why the bytecode feature does not care which one it is,
 * only that the function's resolved runtime is nodejs*.
 */
export const FRAMEWORK = "express";

/**
 * projectSlugForApp derives this harness's project slug straight from the
 * deployed app's own directory: unlike scripts/e2e-next, no external test
 * harness hands this one a NEXT_TEST_DIR — deploy.mjs makes appDir a fresh
 * temp directory per run, which is already unique, so that directory is the
 * whole identity. cleanup.mjs re-derives the same slug from the same
 * directory when its state file is lost.
 */
export function projectSlugForApp(appDir) {
  return projectSlug({ runId: process.env.GITHUB_RUN_ID, dir: appDir });
}

/**
 * renderOcelConfig is the ocel.config.ts written into the deployed app: the
 * shared renderer, fixed to this harness's own single app declaration
 * (APP_NAME, FRAMEWORK).
 */
export function renderOcelConfig({ slug, previewDomain }) {
  return renderSharedOcelConfig({ slug, previewDomain, apps: [{ name: APP_NAME, path: ".", framework: FRAMEWORK }] });
}

/**
 * The smoke app's readiness probe, and also the burst target the bytecode
 * assertions hit to force fresh sandboxes. Mirrors smoke-app/src/server.js —
 * a plain node app carries no CDN or edge cache tier in front of it (no
 * framework here registers an edge worker: cloud/edge/framework/registry.go),
 * so unlike scripts/e2e-next's TAG_PROBE_ROUTE this needs no force-dynamic
 * trick to guarantee every request reaches the Lambda.
 */
export const HEALTH_ROUTE = "/health";

/**
 * The correctness probe's route and the marker its body carries. Mirrors
 * smoke-app/src/server.js — assert-correctness.mjs reads them from here so
 * the route and its assertion cannot drift apart.
 */
export const ECHO_ROUTE = "/echo";
export const ECHO_MARKER = "ocel-e2e-node-smoke-v1";

/**
 * echoValue names one run's probe so a correctness check can tell its own
 * request's response apart from a stale or unrelated one.
 */
export function echoValue(stamp) {
  return `ocel-e2e-node-echo-${stamp}`;
}

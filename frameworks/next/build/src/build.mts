import { spawn } from "node:child_process";
import { realpathSync } from "node:fs";
import path from "node:path";
import { FUNCTION_ARCH_ENV } from "@framework/node-build/output";
import { Refusal } from "@framework/node-build/refusal";
import {
  type BuildLog,
  type OutputBuild,
  prependScriptBins,
  runAdapterBuild,
} from "@framework/node-build/script";

export interface NextBuild extends OutputBuild {
  buildId: string;
  edgeRunsCode?: boolean;
  edgeCacheTagLimits?: { tagBytes: number; tags: number; headerBytes: number };
  allowDegraded?: string[];
  publicKeys?: string[];
  maxFunctionBytes?: number;
  arch?: string;
}

const ADAPTER_PATH_ENV = "NEXT_ADAPTER_PATH";
const DEPLOYMENT_ID_ENV = "NEXT_DEPLOYMENT_ID";

export async function buildNext(app: NextBuild, adapterPath: string, log: BuildLog): Promise<void> {
  await runAdapterBuild(
    app,
    {
      framework: "next",
      name: "ocel's Next adapter",
      setup: `Next runs the adapter ${ADAPTER_PATH_ENV} names from 16.2.10, so upgrade next to 16.2.10 or later`,
      upgrade: "Reinstall this CLI, which carries the adapter",
      refuseBuild: (env) => refuseOwnAdapter(app, adapterPath, env),
    },
    {
      OCEL_EDGE_RUNS_CODE: app.edgeRunsCode ? "1" : "",
      OCEL_EDGE_TAG_LIMITS: app.edgeCacheTagLimits ? JSON.stringify(app.edgeCacheTagLimits) : "",
      OCEL_ALLOW_DEGRADED: (app.allowDegraded ?? []).join(","),
      OCEL_PUBLIC_KEYS: (app.publicKeys ?? []).join(","),
      OCEL_MAX_FUNCTION_BYTES: app.maxFunctionBytes ? String(app.maxFunctionBytes) : "",
      [FUNCTION_ARCH_ENV]: app.arch ?? "",
      [ADAPTER_PATH_ENV]: adapterPath,
      [DEPLOYMENT_ID_ENV]: app.buildId,
    },
    log,
  );
  log.print(`ocel: Next app "${app.name}" built`);
}

async function refuseOwnAdapter(
  app: NextBuild,
  adapterPath: string,
  env: Record<string, string>,
): Promise<void> {
  const loaded = await readLoadedAdapter(app.cwd, env);
  if (!loaded?.adapterPath || loaded.adapterPath === realpathSync(adapterPath)) return;
  const file = path.relative(app.cwd, loaded.configFile);
  throw new Refusal(
    `ocel: app "${app.name}" sets adapterPath to ${loaded.adapterPath} in ${file}, and next build then runs that adapter in place of ocel's, which writes the output ocel deploys: delete adapterPath from ${file}`,
  );
}

interface LoadedAdapter {
  adapterPath: string;
  configFile: string;
}

const READ_LOADED_ADAPTER = `
const { createRequire } = require("node:module");
const { realpathSync } = require("node:fs");
const path = require("node:path");
const fromApp = createRequire(path.join(process.cwd(), "package.json"));
const configModule = fromApp.resolve("next/dist/server/config");
const { PHASE_PRODUCTION_BUILD } = fromApp("next/constants");
require(configModule).default(PHASE_PRODUCTION_BUILD, process.cwd()).then((config) => {
  const adapterPath = config.adapterPath
    ? realpathSync(createRequire(configModule).resolve(config.adapterPath))
    : "";
  process.send({ adapterPath, configFile: config.configFile ?? "" }, () => process.exit(0));
});
`;

async function readLoadedAdapter(
  cwd: string,
  env: Record<string, string>,
): Promise<LoadedAdapter | undefined> {
  return await new Promise((resolve) => {
    let loaded: LoadedAdapter | undefined;
    const child = spawn("node", ["-e", READ_LOADED_ADAPTER], {
      cwd,
      env: { ...env, PATH: prependScriptBins(cwd, env.PATH) },
      stdio: ["ignore", "ignore", "ignore", "ipc"],
    });
    child.on("message", (msg) => {
      loaded = msg as LoadedAdapter;
    });
    child.on("error", () => resolve(undefined));
    child.on("exit", () => resolve(loaded));
  });
}

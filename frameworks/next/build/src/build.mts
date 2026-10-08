import { runBuildScript, type ScriptBuild } from "@framework/node-build/script";

export interface NextBuild extends ScriptBuild {
  outputDir: string;
  buildId: string;
  folder?: string;
  edgeKind?: string;
  allowDegraded?: string[];
  maxFunctionBytes?: number;
}

const ADAPTER_PATH_ENV = "NEXT_ADAPTER_PATH";
const DEPLOYMENT_ID_ENV = "NEXT_DEPLOYMENT_ID";

export async function buildNext(app: NextBuild, adapterPath: string): Promise<void> {
  await runBuildScript(app, {
    NODE_ENV: "production",
    OCEL_APP_NAME: app.name,
    OCEL_OUTPUT_DIR: app.outputDir,
    OCEL_APP_FOLDER: app.folder ?? "",
    OCEL_EDGE_KIND: app.edgeKind ?? "",
    OCEL_ALLOW_DEGRADED: (app.allowDegraded ?? []).join(","),
    OCEL_MAX_FUNCTION_BYTES: app.maxFunctionBytes ? String(app.maxFunctionBytes) : "",
    [ADAPTER_PATH_ENV]: adapterPath,
    [DEPLOYMENT_ID_ENV]: app.buildId,
  });
  process.stderr.write(`ocel: Next app "${app.name}" built\n`);
}

import { type OutputBuild, runAdapterBuild } from "@framework/node-build/script";

export interface NextBuild extends OutputBuild {
  buildId: string;
  edgeKind?: string;
  allowDegraded?: string[];
  maxFunctionBytes?: number;
}

const ADAPTER_PATH_ENV = "NEXT_ADAPTER_PATH";
const DEPLOYMENT_ID_ENV = "NEXT_DEPLOYMENT_ID";

export async function buildNext(app: NextBuild, adapterPath: string): Promise<void> {
  await runAdapterBuild(
    app,
    {
      framework: "next",
      name: "ocel's Next adapter",
      setup: `Next runs the adapter ${ADAPTER_PATH_ENV} names from 16.2.10, so upgrade next to 16.2.10 or later`,
      upgrade: "Reinstall this CLI, which carries the adapter",
    },
    {
      OCEL_EDGE_KIND: app.edgeKind ?? "",
      OCEL_ALLOW_DEGRADED: (app.allowDegraded ?? []).join(","),
      OCEL_MAX_FUNCTION_BYTES: app.maxFunctionBytes ? String(app.maxFunctionBytes) : "",
      [ADAPTER_PATH_ENV]: adapterPath,
      [DEPLOYMENT_ID_ENV]: app.buildId,
    },
  );
  process.stderr.write(`ocel: Next app "${app.name}" built\n`);
}

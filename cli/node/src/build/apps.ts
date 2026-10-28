import type { NextBuild } from "@framework/next-build";
import type { BuildLog } from "@framework/node-build/script";
import type { TraceRequest } from "@framework/node-build/trace";
import type { SvelteKitBuild } from "@framework/sveltekit-build";
import { printOutput, withSpan } from "../protocol.js";
import { inSpan } from "../tracing/tracing.js";

export type AppBuild =
  | ({ framework: "next" } & NextBuild)
  | ({ framework: "sveltekit" } & SvelteKitBuild)
  | ({ framework: "node"; name: string } & TraceRequest);

export interface Adapters {
  next(app: NextBuild, log: BuildLog): Promise<void>;
  sveltekit(app: SvelteKitBuild, log: BuildLog): Promise<void>;
  node(trace: TraceRequest): Promise<void>;
}

export async function buildApps(
  apps: AppBuild[],
  adapters: Adapters,
  concurrency: number,
): Promise<void> {
  const byDir = new Map<string, AppBuild[]>();
  for (const app of apps) byDir.set(app.cwd, [...(byDir.get(app.cwd) ?? []), app]);
  const queue = [...byDir.values()];
  let failure: { err: unknown } | undefined;
  const buildQueued = async (): Promise<void> => {
    for (let sameDir = queue.shift(); sameDir; sameDir = queue.shift()) {
      for (const app of sameDir) {
        if (failure) return;
        try {
          await withSpan("build", app.name, () =>
            inSpan(
              "build app",
              {
                "ocel.app": app.name,
                "ocel.framework": app.framework,
                "app.directory": app.cwd,
              },
              () => buildApp(app, adapters),
            ),
          );
        } catch (err) {
          failure ??= { err };
        }
      }
    }
  };
  await Promise.all(Array.from({ length: Math.max(1, concurrency) }, buildQueued));
  if (failure) throw failure.err;
}

function buildApp(app: AppBuild, adapters: Adapters): Promise<void> {
  const log: BuildLog = {
    print: (text, partial) => printOutput(app.name, text, partial),
  };
  switch (app.framework) {
    case "next":
      return adapters.next(app, log);
    case "sveltekit":
      return adapters.sveltekit(app, log);
    case "node":
      return adapters.node(app);
    default: {
      const unbuilt: { name: string; framework: string } = app;
      throw new Error(
        `ocel: app "${unbuilt.name}" names framework "${unbuilt.framework}", which the node half of the CLI does not build`,
      );
    }
  }
}

import type { NextBuild } from "@framework/next-build";
import type { TraceRequest } from "@framework/node-build/trace";
import type { SvelteKitBuild } from "@framework/sveltekit-build";
import { withSpan } from "./protocol.js";

export type AppBuild =
  | ({ framework: "next" } & NextBuild)
  | ({ framework: "sveltekit" } & SvelteKitBuild)
  | ({ framework: "node"; name: string } & TraceRequest);

export interface Adapters {
  next(app: NextBuild): Promise<void>;
  sveltekit(app: SvelteKitBuild): Promise<void>;
  node(trace: TraceRequest): Promise<void>;
}

export async function buildApps(apps: AppBuild[], adapters: Adapters): Promise<void> {
  for (const app of apps) {
    await withSpan("build", app.name, () => buildApp(app, adapters));
  }
}

function buildApp(app: AppBuild, adapters: Adapters): Promise<void> {
  switch (app.framework) {
    case "next":
      return adapters.next(app);
    case "sveltekit":
      return adapters.sveltekit(app);
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

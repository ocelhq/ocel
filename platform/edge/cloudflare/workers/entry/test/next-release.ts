import type { NextRouteTable } from "@framework/next-protocol/route-table";
import type { AssetBucket } from "@framework/next-router/assets";
import type { PurgeMode } from "@platform/edge-contract/purge";
import { type ResolveBase, resolveServe, type ServeFetch } from "../src/index";
import type { RaisedTag } from "../src/purge-backlog";
import type { ReleaseRecord } from "../src/releases";
import type { ServeFactory } from "../src/serve-objects";
import type { ServingPorts } from "../src/serving";
import { answerEveryRecordWith } from "./origin-deps";
import { resolveBase, servingPorts } from "./resolve-base";
import { objectStoreHolding, routeTableKey } from "./route-table-store";

export const RELEASE_TOKEN = "r0a1b2c3d";

export const ISR_PREFIX = `prod/p1/web/${RELEASE_TOKEN}/isr`;

export const UNTOKENED_ISR_PREFIX = "prod/p1/web/build-1";

export const EMPTY_ROUTES = {
  beforeMiddleware: [],
  beforeFiles: [],
  afterFiles: [],
  dynamicRoutes: [],
  onMatch: [],
  fallback: [],
};

export interface NextRelease {
  table?: Partial<NextRouteTable>;
  record?: Partial<ReleaseRecord>;
  assets?: Record<string, string>;
  edgeBundle?: string;
  serve?: ServeFactory;
  serving?: Partial<ServingPorts>;
  fetch?: typeof fetch;
  loader?: WorkerLoader;
  base?: Partial<ResolveBase>;
}

let edgeBundles = 0;

function nextRecord(over: Partial<ReleaseRecord> = {}): ReleaseRecord {
  return {
    app: "web",
    framework: "next",
    release: "deploy-1",
    buildId: "d1",
    routeTable: { format: "next", key: routeTableKey() },
    functionUrls: {},
    assetPrefix: "",
    isrPrefix: ISR_PREFIX,
    createdAt: 1_000,
    ...over,
  };
}

function nextTable(over: Partial<NextRouteTable> = {}): NextRouteTable {
  const dispatch = over.dispatch ?? {};
  return {
    rootFunction: "",
    buildId: "t",
    basePath: "",
    pathnames: Object.keys(dispatch),
    routes: EMPTY_ROUTES,
    dispatch,
    ...over,
  };
}

function assetBucket(files: Record<string, string>, assetPrefix: string): AssetBucket {
  return {
    async get(key) {
      if (!key.startsWith(assetPrefix)) return null;
      const body = files[key.slice(assetPrefix.length)];
      if (body === undefined) return null;
      return { body: new Blob([body]).stream() };
    },
  };
}

export function servedNextRelease(release: NextRelease = {}): ServeFetch {
  const objects: Record<string, NextRouteTable | string> = {
    [routeTableKey()]: nextTable(release.table),
  };
  const bundled: Partial<ReleaseRecord> = {};
  if (release.edgeBundle !== undefined) {
    const id = `edge-${edgeBundles++}`;
    const bundleKey = `prod/p1/web/${id}/edge/bundle.json`;
    objects[bundleKey] = release.edgeBundle;
    bundled.edgeWorkers = {
      bundleKey,
      id,
      compatDate: "2026-03-10",
      compatFlags: ["nodejs_compat"],
    };
  }
  const record = nextRecord({ ...bundled, ...release.record });
  const base = resolveBase({
    fetch: release.fetch,
    routeTableStore: objectStoreHolding(objects),
    assetStore: {
      store: assetBucket(release.assets ?? {}, record.assetPrefix),
      cache: { match: async () => undefined, put: async () => {} },
      waitUntil: () => {},
    },
    serving: { ...servingPorts(release.serve), ...release.serving },
    ...(release.loader && { edgeRuntime: { loader: release.loader } }),
    ...release.base,
  });
  const binding = answerEveryRecordWith(async () => ({
    kind: "record",
    release: record.release,
    record,
  }));
  return async (request) => {
    const served = await resolveServe(
      { binding, slug: "p1", host: new URL(request.url).host, app: record.app },
      base,
    );
    return served instanceof Response ? served : served(request);
  };
}

interface RaisedGroup {
  mode: PurgeMode;
  tags: string[];
}

export function heldPurgeQueue(): {
  queue: ServingPorts["queue"];
  raised: RaisedGroup[];
  release(): void;
} {
  const raised: RaisedGroup[] = [];
  let release = () => {};
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  return {
    raised,
    release: () => release(),
    queue: {
      raise: async (_scope, mode, tags: RaisedTag[]) => {
        raised.push({ mode, tags: tags.map(({ tag }) => tag) });
        await held;
      },
    },
  };
}

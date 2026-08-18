import type { RoutingManifest } from "@framework/next-protocol/routing-manifest";

import type { RouteDeps } from "../src/index.mjs";
import type { AssetBucket } from "../src/assets.mjs";

export type TestManifest = Omit<RoutingManifest, "entry"> & { entry?: string };

export type TestRouteDeps = Omit<Partial<RouteDeps>, "manifest"> & {
  manifest?: TestManifest;
};

export function assetStoreServing(
  files: Record<string, string>,
): RouteDeps["assetStore"] {
  const store: AssetBucket = {
    async get(key) {
      const body = files[key];
      if (body === undefined) return null;
      return { body: new Blob([body]).stream() };
    },
  };
  return {
    store,
    assetPrefix: "",
    cache: { match: async () => undefined, put: async () => {} },
    waitUntil: () => {},
  };
}

export function noAssets(): RouteDeps["assetStore"] {
  return {
    assetPrefix: "",
    cache: { match: async () => undefined, put: async () => {} },
    waitUntil: () => {},
  };
}

export function baseDeps(overrides: TestRouteDeps = {}): RouteDeps {
  const { manifest, ...rest } = overrides;
  return {
    functionUrls: {},
    slug: "p1",
    deploymentId: "d1",
    app: "web",
    assetStore: noAssets(),
    ...rest,
    manifest: {
      entry: "",
      buildId: "test",
      basePath: "",
      pathnames: [],
      routes: {},
      dispatch: {},
      ...manifest,
    },
  };
}

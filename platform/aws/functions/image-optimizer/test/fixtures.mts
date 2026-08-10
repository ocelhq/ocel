import { createHash } from "node:crypto";
import type { CompiledImageConfig, ImageOriginRequest } from "../src/contract.mjs";
import type { ObjectStore, StoredObject } from "../src/store.mjs";

export function imageConfig(
  overrides: Partial<CompiledImageConfig> = {},
): CompiledImageConfig {
  return {
    path: "/_next/image",
    deviceSizes: [640, 750, 828, 1080, 1200, 1920, 2048, 3840],
    imageSizes: [32, 48, 64, 96, 128, 256, 384],
    qualities: [75],
    formats: ["image/webp"],
    domains: [],
    minimumCacheTTL: 14400,
    maximumRedirects: 3,
    maximumResponseBody: 8 * 1024 * 1024,
    dangerouslyAllowSVG: false,
    dangerouslyAllowLocalIP: false,
    contentSecurityPolicy:
      "script-src 'none'; frame-src 'none'; sandbox;",
    contentDispositionType: "attachment",
    remotePatterns: [
      { protocol: "https", hostname: "^cdn\\.example\\.com$", pathname: "^\\/.*$" },
    ],
    localPatterns: [{ pathname: "^\\/.*$", search: "" }],
    ...overrides,
  };
}

export function serialize(config: CompiledImageConfig): string {
  return JSON.stringify(config, (_key, value) =>
    value && typeof value === "object" && !Array.isArray(value)
      ? Object.fromEntries(Object.entries(value).sort(([a], [b]) => (a < b ? -1 : 1)))
      : value,
  );
}

export function configHash(config: CompiledImageConfig): string {
  return createHash("sha256").update(serialize(config)).digest("hex");
}

export function payload(
  config: CompiledImageConfig,
  overrides: Partial<ImageOriginRequest> = {},
): ImageOriginRequest {
  return {
    slug: "proj1",
    app: "web",
    buildId: "build-1",
    url: "/logo.png",
    w: 640,
    q: 75,
    accept: "image/webp,image/avif,*/*",
    mimeType: "image/webp",
    configHash: configHash(config),
    ...overrides,
  };
}

export interface FakeStore extends ObjectStore {
  objects: Map<string, StoredObject>;
  reads: string[];
  put(key: string, object: Partial<StoredObject> & { bytes: Uint8Array }): void;
}

export function fakeStore(): FakeStore {
  const objects = new Map<string, StoredObject>();
  const reads: string[] = [];
  return {
    objects,
    reads,
    put(key, object) {
      objects.set(key, { cacheControl: null, etag: null, ...object });
    },
    async get(key, limit) {
      reads.push(key);
      const object = objects.get(key);
      if (!object) return undefined;
      if (object.bytes.byteLength > limit) {
        throw new Error(`object ${key} exceeds ${limit} bytes`);
      }
      return object;
    },
  };
}

export function storeWithConfig(config: CompiledImageConfig): FakeStore {
  const store = fakeStore();
  store.put(`image-config/proj1/web/build-1.json`, {
    bytes: new TextEncoder().encode(serialize(config)),
  });
  return store;
}

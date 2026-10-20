import { describe, expect, it } from "vitest";
import fixture from "../fixtures/storage-keys.json" with { type: "json" };

import {
  assetPrefix,
  bucketVars,
  formatStorageCoordinate,
  isStorageCoordinate,
  type ObjectKind,
  objectKey,
  parseAssetPrefix,
  parseObjectKey,
  parseStorageCoordinate,
  readStorageCoordinate,
  storageCoordinateVar,
  storagePrefix,
} from "../src/storage-key.mjs";

const release = { project: "shop", env: "prod", app: "web", release: "r1a2b3c4d" };

describe("storage keys", () => {
  it("parses every key the Go builder writes to its kind, coordinate and rest", () => {
    for (const { key, ...want } of fixture.keys) {
      expect(parseObjectKey(key), key).toEqual(want);
    }
  });

  it("builds every key from its kind, coordinate and rest", () => {
    for (const { key, kind, rest, ...coordinate } of fixture.keys) {
      expect(objectKey(kind as ObjectKind, coordinate, rest)).toBe(key);
    }
  });

  it("names a release's prefix of a kind as the kind then its coordinate", () => {
    for (const { kind, coordinate, prefix } of fixture.prefixes) {
      expect(storagePrefix(kind as ObjectKind, coordinate)).toBe(prefix);
    }
  });

  it("refuses a key outside every coordinate-keyed kind, or one naming no object", () => {
    for (const key of fixture.invalid) {
      expect(parseObjectKey(key), key).toBeUndefined();
    }
  });

  it("reads the storage coordinate the origin hands the runtime", () => {
    for (const { value, coordinate } of fixture.coordinates) {
      expect(parseStorageCoordinate(value)).toEqual(coordinate);
      expect(formatStorageCoordinate(coordinate)).toBe(value);
      expect(isStorageCoordinate(coordinate)).toBe(true);
    }
    for (const value of fixture.invalidCoordinates) {
      expect(parseStorageCoordinate(value), value).toBeUndefined();
    }
  });

  it("refuses a coordinate with a field no slug or release token could be", () => {
    expect(isStorageCoordinate({ ...release, env: "pr--7" })).toBe(false);
    expect(isStorageCoordinate({ ...release, release: "release1" })).toBe(false);
  });
});

describe("the router's asset prefix", () => {
  it("is the release's assets prefix that an absolute pathname follows", () => {
    expect(`${assetPrefix(release)}/_next/static/a.js`).toBe(
      objectKey("assets", release, "_next/static/a.js"),
    );
  });

  it("reads back to the coordinate it names, and to nothing outside the assets kind", () => {
    expect(parseAssetPrefix(assetPrefix(release))).toEqual(release);
    expect(parseAssetPrefix("pages/shop/prod/web/r1a2b3c4d")).toBeUndefined();
    expect(parseAssetPrefix("assets/shop/prod/web")).toBeUndefined();
    expect(parseAssetPrefix(`${assetPrefix(release)}/`)).toBeUndefined();
  });
});

describe("the runtime's storage env", () => {
  it("names the variables the Go origin sets", () => {
    expect(storageCoordinateVar).toBe("OCEL_STORAGE_COORDINATE");
    expect(bucketVars).toEqual({
      artifacts: "OCEL_ARTIFACTS_BUCKET",
      edge: "OCEL_EDGE_BUCKET",
      cache: "OCEL_CACHE_BUCKET",
    });
  });

  it("reads the coordinate the origin set, and nothing when it set none", () => {
    expect(readStorageCoordinate({ OCEL_STORAGE_COORDINATE: "shop/prod/web/r1a2b3c4d" })).toEqual(
      release,
    );
    expect(readStorageCoordinate({})).toBeUndefined();
  });

  it("throws on a coordinate no origin could have set", () => {
    expect(() => readStorageCoordinate({ OCEL_STORAGE_COORDINATE: "shop/prod/web" })).toThrow(
      "OCEL_STORAGE_COORDINATE",
    );
  });
});

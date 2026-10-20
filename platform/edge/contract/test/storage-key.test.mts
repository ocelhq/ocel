import { describe, expect, it } from "vitest";
import fixture from "../fixtures/storage-keys.json" with { type: "json" };

import {
  formatStorageCoordinate,
  isStorageCoordinate,
  type ObjectKind,
  objectKey,
  parseObjectKey,
  parseStorageCoordinate,
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

  it("refuses a value that is no coordinate at all", () => {
    for (const value of [undefined, null, "shop/prod/web/r1a2b3c4d", { ...release, app: 7 }, {}]) {
      expect(isStorageCoordinate(value), JSON.stringify(value)).toBe(false);
    }
  });
});

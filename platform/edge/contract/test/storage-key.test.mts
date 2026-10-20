import { describe, expect, it } from "vitest";
import fixture from "../fixtures/storage-keys.json" with { type: "json" };

import {
  type ObjectKind,
  objectKey,
  pagesPrefix,
  parseObjectKey,
  parseStorageCoordinate,
  storagePrefix,
} from "../src/storage-key.mjs";

describe("storage keys", () => {
  it("parses every key the Go builder writes to its kind, coordinate and rest", () => {
    for (const { key, ...want } of fixture.keys) {
      expect(parseObjectKey(key), key).toEqual(want);
    }
  });

  it("builds every key from its kind, coordinate and rest", () => {
    for (const { key, kind, rest, ...coordinate } of fixture.keys) {
      if (rest === "") continue;
      expect(objectKey(kind as ObjectKind, coordinate, rest)).toBe(key);
    }
  });

  it("stops a storage prefix at the first field the coordinate leaves out", () => {
    for (const { kind, coordinate, prefix } of fixture.prefixes) {
      expect(storagePrefix(kind as ObjectKind, coordinate)).toBe(prefix);
    }
  });

  it("refuses a key outside every coordinate-keyed kind", () => {
    for (const key of fixture.invalid) {
      expect(parseObjectKey(key), key).toBeUndefined();
    }
  });

  it("reads the storage coordinate the origin hands the runtime", () => {
    for (const { value, coordinate } of fixture.coordinates) {
      expect(parseStorageCoordinate(value)).toEqual(coordinate);
    }
    for (const value of fixture.invalidCoordinates) {
      expect(parseStorageCoordinate(value), value).toBeUndefined();
    }
  });

  it("names a release's pages prefix without a trailing slash", () => {
    expect(pagesPrefix({ project: "shop", env: "prod", app: "web", release: "r1a2b3c4d" })).toBe(
      "pages/shop/prod/web/r1a2b3c4d",
    );
  });
});

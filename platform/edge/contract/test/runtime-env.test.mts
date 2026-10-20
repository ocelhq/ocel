import { describe, expect, it } from "vitest";
import fixture from "../fixtures/runtime-env.json" with { type: "json" };

import {
  bucketVars,
  buildIdVar,
  isrVar,
  readStorageCoordinate,
  routerKindVar,
  routeTableVar,
  storageCoordinateVar,
} from "../src/runtime-env.mjs";

describe("the runtime's env", () => {
  it("names every variable as the Go origin sets it", () => {
    expect({
      storageCoordinate: storageCoordinateVar,
      artifactsBucket: bucketVars.artifacts,
      edgeBucket: bucketVars.edge,
      cacheBucket: bucketVars.cache,
      isr: isrVar,
      routerKind: routerKindVar,
      routeTable: routeTableVar,
      buildId: buildIdVar,
    }).toEqual(fixture);
  });
});

const release = { project: "shop", env: "prod", app: "web", release: "r1a2b3c4d" };

describe("the runtime's storage env", () => {
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

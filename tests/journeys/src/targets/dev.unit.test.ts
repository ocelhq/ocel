import { describe, expect, it } from "bun:test";
import { devProject, startedResources } from "./dev";

describe("the project a dev stack is labelled with", () => {
  it("is the name ocel dev derives from the directory, so the harness can find what it started", () => {
    expect(devProject("/work/trees/sdk-node/My Shop")).toBe("my-shop-2d2cdb07");
  });

  it("differs between two trees whose last segment is the same", () => {
    expect(devProject("/work/a/node")).not.toBe(devProject("/work/b/node"));
  });
});

describe("whether ocel dev started resources for a fixture", () => {
  it("is read off what ocel dev said, so a fixture with a bucket and no schema is still required to have a traceable stack", () => {
    const said = [
      "resolved PORT from .env.",
      'bucket "uploads" → floci s3://dev-uploads-1a2b3c4d @ 127.0.0.1:49153',
    ];
    expect(startedResources(said.join("\n"))).toBe(true);
  });

  it("is also true of a declared postgres", () => {
    expect(startedResources('postgres "main" → postgres:17 @ 127.0.0.1:49154\n')).toBe(true);
  });

  it("is false for an app that declared nothing", () => {
    expect(startedResources("resolved PORT from .env.\nlistening on 3000\n")).toBe(false);
  });
});

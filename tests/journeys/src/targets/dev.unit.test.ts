import { describe, expect, it } from "bun:test";
import { devProject, keptRunning, startedResources, volumesIn } from "./dev";

describe("the containers a restart kept running", () => {
  it("names each container whose start time did not move", () => {
    const before = new Map([
      ["/dev-kv-cache", "2026-10-01T10:00:00Z"],
      ["/dev-kv-bounded", "2026-10-01T10:00:01Z"],
    ]);
    const after = new Map([
      ["/dev-kv-cache", "2026-10-01T10:05:00Z"],
      ["/dev-kv-bounded", "2026-10-01T10:00:01Z"],
    ]);
    expect(keptRunning(before, after)).toEqual(["/dev-kv-bounded"]);
  });

  it("names none when every container started again or is new", () => {
    const before = new Map([["/dev-kv-cache", "2026-10-01T10:00:00Z"]]);
    const after = new Map([
      ["/dev-kv-cache", "2026-10-01T10:05:00Z"],
      ["/dev-kv-evicting", "2026-10-01T10:05:01Z"],
    ]);
    expect(keptRunning(before, after)).toEqual([]);
  });
});

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
      "INFO  resolved PORT from .env.",
      'INFO  bucket "uploads" → floci s3://dev-uploads-1a2b3c4d @ 127.0.0.1:49153',
    ];
    expect(startedResources(said.join("\n"))).toBe(true);
  });

  it("is also true of a declared postgres", () => {
    expect(startedResources('INFO  postgres "main" → postgres:17 @ 127.0.0.1:49154\n')).toBe(true);
  });

  it("is also true of a declared kv store", () => {
    expect(startedResources('INFO  kv "cache" → valkey:9 @ 127.0.0.1:49155\n')).toBe(true);
  });

  it("is false for an app that declared nothing", () => {
    expect(startedResources("INFO  resolved PORT from .env.\nlistening on 3000\n")).toBe(false);
  });
});

describe("the volumes docker inspect names", () => {
  it("names each volume the containers mount once", () => {
    expect(volumesIn("dev-kv-cache-9 3f2a \ndev-postgres-main-17 3f2a \n")).toEqual([
      "dev-kv-cache-9",
      "3f2a",
      "dev-postgres-main-17",
    ]);
  });

  it("names none for containers that mount no volume", () => {
    expect(volumesIn("\n\n")).toEqual([]);
  });
});

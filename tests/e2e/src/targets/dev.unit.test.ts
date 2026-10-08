import { describe, expect, it } from "bun:test";
import { createServer } from "node:http";
import type { AddressInfo } from "node:net";
import {
  answersHealth,
  devProject,
  findKeptRunning,
  HEALTH_TIMEOUT_MS,
  healthDeadline,
  RESOLVE_TIMEOUT_MS,
  resolvedEnvironment,
  startedResources,
  volumesIn,
} from "./dev";

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
    expect(findKeptRunning(before, after)).toEqual(["/dev-kv-bounded"]);
  });

  it("names none when every container started again or is new", () => {
    const before = new Map([["/dev-kv-cache", "2026-10-01T10:00:00Z"]]);
    const after = new Map([
      ["/dev-kv-cache", "2026-10-01T10:05:00Z"],
      ["/dev-kv-evicting", "2026-10-01T10:05:01Z"],
    ]);
    expect(findKeptRunning(before, after)).toEqual([]);
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

describe("whether ocel dev is past resolving the app's environment", () => {
  const building =
    "INFO  Still running: Resolving the app's environment — 0/1 done, 1m32s elapsed\n";

  it("is false while it still builds and resolves", () => {
    expect(resolvedEnvironment(building)).toBe(false);
  });

  it("is true once it resolved the app's environment", () => {
    expect(
      resolvedEnvironment(`${building}INFO  ✓ Resolved the app's environment in 2m05s\n`),
    ).toBe(true);
  });

  it("is true once it resolved the app's environment in colour", () => {
    expect(
      resolvedEnvironment(
        "\x1b[90mINFO \x1b[0m \x1b[32m✓\x1b[0m Resolved the app's environment\x1b[90m in <1s\x1b[0m\n",
      ),
    ).toBe(true);
  });

  it("is true once a second ocel dev connected to the running one", () => {
    expect(resolvedEnvironment("INFO  ✓ Connected to the running `ocel dev` in 0s\n")).toBe(true);
  });
});

describe("the deadline an app served by ocel dev answers /health by", () => {
  const started = 1_000_000;

  it("runs from the moment ocel dev resolved the app's environment, so a cold build before it spends none of it", () => {
    const resolvedAt = started + 125_000;
    expect(healthDeadline(started, resolvedAt)).toBe(resolvedAt + HEALTH_TIMEOUT_MS);
  });

  it("bounds the build itself while ocel dev is still resolving", () => {
    expect(healthDeadline(started, undefined)).toBe(
      started + RESOLVE_TIMEOUT_MS + HEALTH_TIMEOUT_MS,
    );
  });
});

describe("whether an app answers /health", () => {
  it("is false, and returns, when the app accepts the request and never answers it", async () => {
    const server = createServer(() => undefined);
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    const { port } = server.address() as AddressInfo;
    try {
      const started = Date.now();
      expect(await answersHealth(`http://127.0.0.1:${port}/health`, 200)).toBe(false);
      expect(Date.now() - started).toBeLessThan(5_000);
    } finally {
      server.closeAllConnections();
      server.close();
    }
  });
});

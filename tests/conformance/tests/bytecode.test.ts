import { describe, expect, it, vi } from "vitest";
import {
  downloadPackage,
  findCacheKey,
  readCacheObject,
} from "../src/aws-bytecode";
import { assertBytecodeConformance } from "../src/checks/bytecode";

describe("bytecode conformance", () => {
  it("checks the cache archive, deployed artifact, and cold-start read", async () => {
    const calls: string[] = [];
    await assertBytecodeConformance({
      output: () =>
        "warmed 1/1 bundles\nembedded 1/1 compile caches",
      assertBytecodeArchive: vi.fn(async () => {
        calls.push("archive");
      }),
      assertBytecodeEmbeddedArtifact: vi.fn(async () => {
        calls.push("artifact");
      }),
      assertBytecodeColdStart: vi.fn(async () => {
        calls.push("cold-start");
      }),
    });
    expect(calls).toEqual(["archive", "artifact", "cold-start"]);
  });

  it("rejects an incomplete deploy before inspecting its output", async () => {
    const archive = vi.fn(async () => {});
    await expect(
      assertBytecodeConformance({
        output: () =>
          "warmed 0/1 bundles\nembedded 0/1 compile caches",
        assertBytecodeArchive: archive,
        assertBytecodeEmbeddedArtifact: vi.fn(async () => {}),
        assertBytecodeColdStart: vi.fn(async () => {}),
      }),
    ).rejects.toThrow();
    expect(archive).not.toHaveBeenCalled();
  });
});

describe("bytecode AWS reads", () => {
  it("retries only transient and not-yet-visible cache listings", async () => {
    const clock = fakeClock();
    const command = vi
      .fn<(args: string[]) => string>()
      .mockImplementationOnce(() => {
        throw awsError("SlowDown");
      })
      .mockReturnValueOnce(JSON.stringify({ Contents: [] }))
      .mockReturnValueOnce(
        JSON.stringify({
          Contents: [{ Key: "prefix/node22.20.0-x86_64.tar.gz" }],
        }),
      );
    await expect(
      findCacheKey("bucket", "prefix/", "x86_64", {
        command,
        ...clock.dependencies,
      }),
    ).resolves.toBe("prefix/node22.20.0-x86_64.tar.gz");
    expect(command).toHaveBeenCalledTimes(3);
    expect(clock.waits).toEqual([125, 250]);
  });

  it("fails permanent cache listing errors immediately", async () => {
    const clock = fakeClock();
    const command = vi.fn<(args: string[]) => string>(() => {
      throw awsError("AccessDenied");
    });
    await expect(
      findCacheKey("bucket", "prefix/", "x86_64", {
        command,
        ...clock.dependencies,
      }),
    ).rejects.toThrow(/AccessDenied/);
    expect(command).toHaveBeenCalledOnce();
    expect(clock.waits).toEqual([]);
  });

  it("retries a cache object that is briefly unavailable", async () => {
    const clock = fakeClock();
    const commandBytes = vi
      .fn<(args: string[]) => Buffer>()
      .mockImplementationOnce(() => {
        throw awsError("NoSuchKey");
      })
      .mockReturnValue(Buffer.from("cache"));
    await expect(
      readCacheObject("bucket", "cache.tar.gz", {
        commandBytes,
        ...clock.dependencies,
      }),
    ).resolves.toEqual(Buffer.from("cache"));
    expect(clock.waits).toEqual([125]);
  });

  it("honors Retry-After and bounds each package request", async () => {
    const clock = fakeClock();
    const timeouts: number[] = [];
    const fetcher = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(
        new Response("busy", {
          status: 429,
          headers: { "retry-after": "2" },
        }),
      )
      .mockResolvedValueOnce(new Response("package"));
    await expect(
      downloadPackage("https://example.test/package", "fn", {
        fetch: fetcher,
        timeoutSignal: (milliseconds) => {
          timeouts.push(milliseconds);
          return AbortSignal.abort();
        },
        ...clock.dependencies,
      }),
    ).resolves.toEqual(Buffer.from("package"));
    expect(clock.waits).toEqual([2_000]);
    expect(timeouts).toEqual([10_000, 10_000]);
  });

  it("does not retry a permanent package response", async () => {
    const clock = fakeClock();
    const fetcher = vi
      .fn<typeof fetch>()
      .mockResolvedValue(new Response("denied", { status: 403 }));
    await expect(
      downloadPackage("https://example.test/package", "fn", {
        fetch: fetcher,
        timeoutSignal: AbortSignal.timeout,
        ...clock.dependencies,
      }),
    ).rejects.toThrow(/HTTP 403/);
    expect(fetcher).toHaveBeenCalledOnce();
    expect(clock.waits).toEqual([]);
  });
});

function fakeClock() {
  let now = 0;
  const waits: number[] = [];
  return {
    waits,
    dependencies: {
      now: () => now,
      random: () => 0,
      sleep: async (milliseconds: number) => {
        waits.push(milliseconds);
        now += milliseconds;
      },
    },
  };
}

function awsError(detail: string) {
  return Object.assign(new Error(detail), { stderr: detail });
}

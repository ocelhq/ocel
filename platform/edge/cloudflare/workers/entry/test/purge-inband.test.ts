import type { PurgeMode } from "@platform/edge-contract/purge";
import { describe, expect, it, vi } from "vitest";
import type { PurgeOutcome, RaisedTag } from "../src/purge-backlog";
import { purgeInBand } from "../src/purge-inband";

function ports(
  answer: (mode: PurgeMode, tags: string[]) => PurgeOutcome | Error = () => ({ ok: true }),
) {
  const purged: { mode: PurgeMode; tags: string[] }[] = [];
  const raised: { mode: PurgeMode; tags: RaisedTag[] }[] = [];
  const recorded: { mode: PurgeMode; tags: RaisedTag[] }[] = [];
  return {
    purged,
    raised,
    recorded,
    port: {
      cache: {
        purge: async (mode: PurgeMode, tags: string[]) => {
          purged.push({ mode, tags });
          const outcome = answer(mode, tags);
          if (outcome instanceof Error) throw outcome;
          return outcome;
        },
      },
      queue: {
        raise: async (mode: PurgeMode, tags: RaisedTag[]) => {
          raised.push({ mode, tags });
        },
      },
      times: {
        record: async (mode: PurgeMode, tags: RaisedTag[]) => {
          recorded.push({ mode, tags });
        },
      },
      now: () => 5_000,
    },
  };
}

const refused: PurgeOutcome = { ok: false, codes: [1134], message: "rate limited" };

describe("a purge the origin announced in-band", () => {
  it("purges each group's tags in its own mode, and raises nothing when the purge lands", async () => {
    const { port, purged, raised } = ports();

    await purgeInBand(
      [
        { mode: "invalidate", tags: ["r1|a"] },
        { mode: "purge", tags: ["r1|b", "r1|c"] },
      ],
      port,
    );

    expect(purged).toEqual([
      { mode: "invalidate", tags: ["r1|a"] },
      { mode: "purge", tags: ["r1|b", "r1|c"] },
    ]);
    expect(raised).toEqual([]);
  });

  it("records the time of every tag it purges, whether or not the purge lands", async () => {
    const { port, recorded } = ports();

    await purgeInBand([{ mode: "invalidate", tags: ["r1|a"] }], port);

    expect(recorded).toEqual([{ mode: "invalidate", tags: [{ tag: "r1|a", raisedAt: 5_000 }] }]);
  });

  it("purges nothing itself when the times cannot be recorded, and hands the tags to the purge queue, which records before it purges", async () => {
    const { port, purged, raised } = ports();
    port.times.record = async () => {
      throw new Error("times down");
    };
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});

    try {
      await purgeInBand([{ mode: "purge", tags: ["r1|a"] }], port);
      expect(purged).toEqual([]);
      expect(raised).toEqual([{ mode: "purge", tags: [{ tag: "r1|a", raisedAt: 5_000 }] }]);
    } finally {
      logged.mockRestore();
    }
  });

  it("hands the tags to the purge queue when the account's purge budget refuses them", async () => {
    const { port, raised } = ports(() => refused);

    await purgeInBand([{ mode: "purge", tags: ["r1|a", "r1|b"] }], port);

    expect(raised).toEqual([
      {
        mode: "purge",
        tags: [
          { tag: "r1|a", raisedAt: 5_000 },
          { tag: "r1|b", raisedAt: 5_000 },
        ],
      },
    ]);
  });

  it("hands the tags to the purge queue when the purge call itself throws", async () => {
    const { port, raised } = ports(() => new Error("rpc dropped"));

    await purgeInBand([{ mode: "invalidate", tags: ["r1|a"] }], port);

    expect(raised.map(({ mode }) => mode)).toEqual(["invalidate"]);
  });

  it("purges no more than a hundred tags in a call", async () => {
    const { port, purged } = ports();
    const tags = Array.from({ length: 250 }, (_, i) => `r1|t${i}`);

    await purgeInBand([{ mode: "purge", tags }], port);

    expect(purged.map((call) => call.tags.length)).toEqual([100, 100, 50]);
  });

  it("settles even when the purge queue refuses the tags too", async () => {
    const { port } = ports(() => refused);
    port.queue.raise = async () => {
      throw new Error("queue down");
    };
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});

    try {
      await expect(purgeInBand([{ mode: "purge", tags: ["r1|a"] }], port)).resolves.toBeUndefined();
      expect(logged).toHaveBeenCalled();
    } finally {
      logged.mockRestore();
    }
  });
});

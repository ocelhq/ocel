import type { PurgeMode } from "@platform/edge-contract/purge";
import { describe, expect, it, vi } from "vitest";
import type { RaisedTag } from "../src/purge-backlog";
import { type InBandPurgePorts, purgeInBand } from "../src/purge-inband";
import type { ReleaseScope } from "../src/purge-times";

const scope = { slug: "p1", pagesPrefix: "pages/p1/prod/web/r00000001" };

function ports() {
  const raised: { scope: ReleaseScope; mode: PurgeMode; tags: RaisedTag[] }[] = [];
  const port: InBandPurgePorts = {
    queue: {
      raise: async (scope, mode, tags) => {
        raised.push({ scope, mode, tags });
      },
    },
    scope,
    now: () => 5_000,
  };
  return { raised, port };
}

describe("a purge the origin announced in-band", () => {
  it("raises each group's tags on the purge queue in its own mode, for the release that announced them", async () => {
    const { port, raised } = ports();

    await purgeInBand(
      [
        { mode: "invalidate", tags: ["r1|a"] },
        { mode: "purge", tags: ["r1|b", "r1|c"] },
      ],
      port,
    );

    expect(raised).toEqual([
      { scope, mode: "invalidate", tags: [{ tag: "r1|a", raisedAt: 5_000 }] },
      {
        scope,
        mode: "purge",
        tags: [
          { tag: "r1|b", raisedAt: 5_000 },
          { tag: "r1|c", raisedAt: 5_000 },
        ],
      },
    ]);
  });

  it("raises nothing for a group with no tags", async () => {
    const { port, raised } = ports();

    await purgeInBand([{ mode: "purge", tags: [] }], port);

    expect(raised).toEqual([]);
  });

  it("settles, and logs, when the purge queue refuses the tags", async () => {
    const { port } = ports();
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

import { describe, expect, it } from "bun:test";
import type { PreviewRelease } from "../targets/types";
import {
  type Clock,
  checkOwnReleases,
  checkPruned,
  checkRefusedWithoutIdentity,
  PREVIEW_TITLES,
  previewChecksFor,
} from "./previews";

const page = (id: string) => `<html><p data-ocel="deployment">${id}</p></html>`;

function clockOf(answer: (url: string, at: number) => Response): Clock {
  let now = 0;
  return {
    fetch: (async (input: string | URL | Request) => answer(String(input), now)) as typeof fetch,
    sleep: async (ms) => {
      now += ms;
    },
    now: () => now,
  };
}

const release = (id: string, over: Partial<PreviewRelease> = {}): PreviewRelease => ({
  app: "web",
  urls: [`https://alias-${id}.test`],
  deploymentUrl: `https://${id}.test`,
  buildId: id,
  ...over,
});

const serving = (id: string) => new Response(page(id));

describe("the preview checks a cell walks", () => {
  it("walks the hostname checks behind an edge and the Identity-Aware Proxy refusal on gcp with none", () => {
    const edge = { name: "alb", offeredOn: ["gcp" as const], config: { edge: "alb" as const } };
    const none = { name: "default", offeredOn: ["gcp" as const], config: {} };

    expect(previewChecksFor({ target: "gcp", variant: edge })).toEqual(["own-release", "pruned"]);
    expect(previewChecksFor({ target: "gcp", variant: none })).toEqual(["refused"]);
    expect(Object.keys(PREVIEW_TITLES).sort()).toEqual(["own-release", "pruned", "refused"]);
  });
});

describe("checkOwnReleases", () => {
  it("passes when each deployment serves its own release and the alias serves the newer one", async () => {
    const first = release("A");
    const second = release("B", { urls: ["https://alias.test"] });
    const clock = clockOf((url) => {
      if (url.startsWith("https://A.test")) return serving("A");
      return serving("B");
    });

    await checkOwnReleases(clock, "web", first, second);
  });

  it("fails the own-release check when both deployments serve one release", async () => {
    const clock = clockOf(() => serving("A"));

    await expect(checkOwnReleases(clock, "web", release("A"), release("B"))).rejects.toThrow(
      /https:\/\/B\.test.*B.*A/s,
    );
  });

  it("waits for a fresh deployment hostname to start serving before it fails", async () => {
    const clock = clockOf((url, at) => {
      if (url.startsWith("https://A.test") && at < 30_000) return new Response("", { status: 404 });
      return url.startsWith("https://A.test") ? serving("A") : serving("B");
    });

    await checkOwnReleases(clock, "web", release("A"), release("B"));
  });
});

describe("checkPruned", () => {
  it("passes the prune check once the pruned deployment stops answering with its release", async () => {
    const clock = clockOf((url, at) => {
      if (url.startsWith("https://P.test")) {
        return at < 60_000 ? serving("P") : new Response("", { status: 404 });
      }
      return serving("K");
    });

    expect(await checkPruned(clock, "web", release("P"), release("K"))).toBe("404");
  });

  it("fails the prune check when the pruned deployment keeps serving after the deadline", async () => {
    const clock = clockOf((url) =>
      url.startsWith("https://P.test") ? serving("P") : serving("K"),
    );

    await expect(checkPruned(clock, "web", release("P"), release("K"))).rejects.toThrow(
      /P\.test.*still serves/s,
    );
  });

  it("fails the prune check when the kept deployment stops serving its release", async () => {
    const clock = clockOf((url) =>
      url.startsWith("https://P.test") ? new Response("", { status: 404 }) : serving("other"),
    );

    await expect(checkPruned(clock, "web", release("P"), release("K"))).rejects.toThrow(
      /K\.test.*K/s,
    );
  });
});

describe("checkRefusedWithoutIdentity", () => {
  it("passes the Identity-Aware Proxy check on 302, 401 and 403 and fails on a 200 that carries the app's marker", async () => {
    const statuses = [302, 401, 403];
    const refused = clockOf((url) => {
      const status = statuses[Number(url.at(-1))] ?? 403;
      return new Response("", { status });
    });
    const releases = [0, 1, 2].map((n) =>
      release(`R${n}`, {
        urls: [`https://alias.test/${n}`],
        deploymentUrl: `https://dep.test/${n}`,
      }),
    );
    const seen = await checkRefusedWithoutIdentity(refused, releases);
    expect(Object.values(seen).sort()).toEqual([302, 302, 401, 401, 403, 403]);

    const open = clockOf(() => serving("A"));
    await expect(checkRefusedWithoutIdentity(open, [release("A")])).rejects.toThrow(/200/);
  });
});

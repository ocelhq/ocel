import { describe, expect, it } from "bun:test";
import type { PreviewRelease } from "../targets/types";
import {
  type Clock,
  checkOwnReleases,
  checkPreviewGate,
  checkPruned,
  PREVIEW_TITLES,
  previewChecks,
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
  it("walks the own-release and pruned checks, and the gate after them only when the target hands credentials", () => {
    expect(previewChecks(false)).toEqual(["own-release", "pruned"]);
    expect(previewChecks(true)).toEqual(["own-release", "pruned", "gated"]);
    expect(Object.keys(PREVIEW_TITLES).sort()).toEqual(["gated", "own-release", "pruned"]);
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

describe("checkPreviewGate", () => {
  const credentials = { password: "pw", bypassSecret: "bp" };
  const noindex = { "x-robots-tag": "noindex" };
  const refusal = () =>
    new Response("<form></form>", {
      status: 401,
      headers: { "cache-control": "private, no-store", ...noindex },
    });
  const served = (id: string) => new Response(page(id), { headers: noindex });

  function gatedClock(
    id: string,
    accepts: { cookie?: boolean; basic?: boolean; bypass?: boolean } = {},
  ): Clock {
    const { cookie = true, basic = true, bypass = true } = accepts;
    return {
      fetch: (async (_input: string | URL | Request, init?: RequestInit) => {
        const headers = new Headers(init?.headers);
        if (init?.method === "POST") {
          return new Response(null, {
            status: 303,
            headers: { location: "/", "set-cookie": "__Host-ocel-preview=1.ab; Path=/" },
          });
        }
        const sentCookie = headers.get("cookie")?.startsWith("__Host-ocel-preview=");
        const sentBasic = headers.get("authorization") === `Basic ${btoa("ocel:pw")}`;
        const sentBypass = headers.get("x-ocel-preview-bypass") === "bp";
        if ((sentCookie && cookie) || (sentBasic && basic) || (sentBypass && bypass)) {
          return served(id);
        }
        return refusal();
      }) as typeof fetch,
      sleep: async () => {},
      now: () => 0,
    };
  }

  it("passes when credentials are refused, and each of cookie, Basic and bypass serves the page", async () => {
    await checkPreviewGate(gatedClock("A"), release("A"), credentials);
  });

  it("fails when a request without credentials is served", async () => {
    await expect(
      checkPreviewGate(
        clockOf(() => served("A")),
        release("A"),
        credentials,
      ),
    ).rejects.toThrow(/answered 200 to a request without credentials/);
  });

  it("fails when a refusal may be cached", async () => {
    const clock = clockOf(() => new Response("", { status: 401 }));
    await expect(checkPreviewGate(clock, release("A"), credentials)).rejects.toThrow(/no-store/);
  });

  it("fails when Basic credentials are not accepted", async () => {
    await expect(
      checkPreviewGate(gatedClock("A", { basic: false }), release("A"), credentials),
    ).rejects.toThrow(/Basic/);
  });

  it("fails when the bypass header is not accepted", async () => {
    await expect(
      checkPreviewGate(gatedClock("A", { bypass: false }), release("A"), credentials),
    ).rejects.toThrow(/bypass/);
  });

  it("fails when the session cookie a login returned is not accepted", async () => {
    await expect(
      checkPreviewGate(gatedClock("A", { cookie: false }), release("A"), credentials),
    ).rejects.toThrow(/cookie/);
  });
});

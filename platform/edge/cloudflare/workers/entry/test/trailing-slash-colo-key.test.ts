import { describe, expect, it } from "vitest";

import {
  deps as routerDeps,
  get,
  type Scenario as RouterScenario,
} from "@framework/next-router/test-support/serve-scenario";

import { serve, type RouteDeps } from "../src/index";
import { coloDeps } from "./cache-deps";

interface Scenario extends RouterScenario {
  cache?: RouteDeps["cache"];
}

function deps(scenario: Scenario): RouteDeps {
  return { ...routerDeps(scenario), cache: scenario.cache };
}

describe("the resolved path is what keys the response", () => {
  describe("a prerender route, keyed in the colo cache", () => {
    function coloScenario(trailingSlash: boolean, skip = false) {
      const stored = new Map<string, Response>();
      const pending: Promise<unknown>[] = [];
      let renders = 0;
      const scenario: Scenario = {
        trailingSlash,
        skipTrailingSlashRedirect: skip,
        pages: ["/p"],
        dispatch: { "/p": { kind: "prerender", id: "fn", config: {} } },
        functionUrls: { fn: "https://fn.example.com" },
        fetch: (async () => {
          renders += 1;
          return new Response("prerendered", {
            status: 200,
            headers: { "cache-control": "s-maxage=60" },
          });
        }) as unknown as typeof fetch,
        cache: coloDeps({
          cache: {
            match: async (req: Request) => stored.get(req.url)?.clone(),
            put: async (req: Request, res: Response) => {
              stored.set(req.url, res);
            },
          } as unknown as Cache,
          waitUntil: (p: Promise<unknown>) => {
            pending.push(p);
          },
        }),
      };
      const settle = () => Promise.all(pending.splice(0));
      return { scenario, settle, renders: () => renders, keys: () => [...stored.keys()] };
    }

    it("gives the canonical and the slash-free form one key under trailingSlash: true", async () => {
      const { scenario, settle, renders, keys } = coloScenario(true);
      const d = deps(scenario);

      const first = await serve(get("/p/"), d);
      expect(first.status).toBe(200);
      expect(first.headers.get("x-ocel-cache")).toBe("MISS");
      await settle();

      const redirect = await serve(get("/p"), d);
      expect(redirect.status).toBe(308);
      expect(redirect.headers.get("location")).toBe("/p/");

      const follow = await serve(get("/p/"), d);
      expect(follow.headers.get("x-ocel-cache")).toBe("HIT");
      expect(await follow.text()).toBe("prerendered");
      expect(renders()).toBe(1);
      expect(keys()).toEqual(["https://cache.ocel/p1/web/d1/p"]);
    });

    it("gives both served forms one key under skipTrailingSlashRedirect", async () => {
      const { scenario, settle, renders, keys } = coloScenario(true, true);
      const d = deps(scenario);

      const first = await serve(get("/p/"), d);
      expect(first.headers.get("x-ocel-cache")).toBe("MISS");
      await settle();

      const second = await serve(get("/p"), d);
      expect(second.headers.get("x-ocel-cache")).toBe("HIT");
      expect(await second.text()).toBe("prerendered");
      expect(renders()).toBe(1);
      expect(keys()).toEqual(["https://cache.ocel/p1/web/d1/p"]);
    });

    it("gives two apps of one project a key each", async () => {
      const { scenario, settle, renders, keys } = coloScenario(true, true);
      const web = deps(scenario);
      const admin = { ...web, app: "admin" };

      expect((await serve(get("/p"), web)).headers.get("x-ocel-cache")).toBe("MISS");
      await settle();
      expect((await serve(get("/p"), admin)).headers.get("x-ocel-cache")).toBe("MISS");
      await settle();

      expect(web.deploymentId).toBe(admin.deploymentId);
      expect(web.manifest.buildId).toBe(admin.manifest.buildId);
      expect(renders()).toBe(2);
      expect(keys()).toEqual([
        "https://cache.ocel/p1/web/d1/p",
        "https://cache.ocel/p1/admin/d1/p",
      ]);
    });

    it("gives two deployments of one app a key each", async () => {
      const { scenario, settle, renders, keys } = coloScenario(true, true);
      const first = deps(scenario);
      const second = { ...first, deploymentId: "d2" };

      expect((await serve(get("/p"), first)).headers.get("x-ocel-cache")).toBe("MISS");
      await settle();
      expect((await serve(get("/p"), second)).headers.get("x-ocel-cache")).toBe("MISS");
      await settle();

      expect(first.app).toBe(second.app);
      expect(first.manifest.buildId).toBe(second.manifest.buildId);
      expect(renders()).toBe(2);
      expect(keys()).toEqual([
        "https://cache.ocel/p1/web/d1/p",
        "https://cache.ocel/p1/web/d2/p",
      ]);
    });
  });
});

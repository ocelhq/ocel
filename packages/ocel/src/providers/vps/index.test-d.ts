import { describe, it } from "vitest";

import vpsProvider from "./index.js";

describe("the vps provider's proxy", () => {
  it("is left out when ocel runs its own", () => {
    vpsProvider({ ssh: "prod" });
  });

  it("names a known setup or manual as a string", () => {
    vpsProvider({ ssh: "prod", proxy: "manual" });
    vpsProvider({ ssh: "prod", proxy: "coolify" });
    vpsProvider({ ssh: "prod", proxy: "dokploy" });
  });

  it("spells one setup out as an object keyed by what runs", () => {
    vpsProvider({ ssh: "prod", proxy: { manual: { port: 8480 } } });
    vpsProvider({ ssh: "prod", proxy: { manual: {} } });
    vpsProvider({ ssh: "prod", proxy: { caddy: { directory: "/etc/caddy/sites" } } });
    vpsProvider({
      ssh: "prod",
      proxy: { traefik: { directory: "/traefik/dynamic", resolver: "letsencrypt" } },
    });
  });

  it("names no setup it does not know", () => {
    // @ts-expect-error nginx is routed by hand, as manual
    vpsProvider({ ssh: "prod", proxy: "nginx" });
  });

  it("holds exactly one key", () => {
    vpsProvider({
      ssh: "prod",
      // @ts-expect-error one proxy fronts a box
      proxy: { manual: {}, caddy: { directory: "/etc/caddy/sites" } },
    });
  });

  it("carries what its key needs", () => {
    // @ts-expect-error traefik names the resolver its routers ask for certificates
    vpsProvider({ ssh: "prod", proxy: { traefik: { directory: "/traefik/dynamic" } } });
  });
});

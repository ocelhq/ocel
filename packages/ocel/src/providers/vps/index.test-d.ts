import { describe, it } from "vitest";

import vpsProvider from "./index.js";

describe("the vps provider's proxy", () => {
  it("is left out when ocel runs its own", () => {
    vpsProvider({ ssh: "prod" });
  });

  it("names manual as a string", () => {
    vpsProvider({ ssh: "prod", proxy: "manual" });
  });

  it("spells one proxy out as an object keyed by what runs", () => {
    vpsProvider({ ssh: "prod", proxy: { manual: { port: 8480 } } });
    vpsProvider({ ssh: "prod", proxy: { manual: { port: 8480, network: "coolify" } } });
    vpsProvider({ ssh: "prod", proxy: { manual: {} } });
    vpsProvider({ ssh: "prod", proxy: { caddy: { directory: "/etc/caddy/sites" } } });
    vpsProvider({
      ssh: "prod",
      proxy: {
        caddy: {
          directory: "/etc/caddy/ocel.d",
          container: "caddy",
          config: "/etc/caddy/Caddyfile",
          network: "web",
        },
      },
    });
    vpsProvider({
      ssh: "prod",
      proxy: { traefik: { directory: "/traefik/dynamic", resolver: "letsencrypt" } },
    });
    vpsProvider({
      ssh: "prod",
      proxy: {
        traefik: {
          directory: "/etc/traefik/dynamic",
          resolver: "letsencrypt",
          previewResolver: "cloudflare",
          entrypoints: { http: "web", https: "websecure" },
          network: "traefik",
        },
      },
    });
  });

  it("fills in how a host tool runs its proxy from a preset", () => {
    vpsProvider({ ssh: "prod", proxy: { traefik: { preset: "coolify" } } });
    vpsProvider({ ssh: "prod", proxy: { traefik: { preset: "dokploy" } } });
    vpsProvider({ ssh: "prod", proxy: { traefik: { preset: "coolify", resolver: "le-dns" } } });
    vpsProvider({ ssh: "prod", proxy: { caddy: { preset: "coolify" } } });
    vpsProvider({ ssh: "prod", proxy: { traefik: { preset: "coolify", port: 9000 } } });
    vpsProvider({ ssh: "prod", proxy: { caddy: { preset: "coolify", port: 9000 } } });
  });

  it("names no proxy it does not know", () => {
    // @ts-expect-error nginx is routed by hand, as manual
    vpsProvider({ ssh: "prod", proxy: "nginx" });
    // @ts-expect-error coolify runs a proxy and is none; name it as a traefik or caddy preset
    vpsProvider({ ssh: "prod", proxy: "coolify" });
  });

  it("holds exactly one key", () => {
    vpsProvider({
      ssh: "prod",
      // @ts-expect-error one proxy fronts a box
      proxy: { manual: {}, caddy: { directory: "/etc/caddy/sites" } },
    });
  });

  it("carries what its key needs, or a preset that fills it", () => {
    // @ts-expect-error traefik names the directory and resolver, or a preset
    vpsProvider({ ssh: "prod", proxy: { traefik: {} } });
    // @ts-expect-error traefik names the resolver its routers ask for certificates
    vpsProvider({ ssh: "prod", proxy: { traefik: { directory: "/traefik/dynamic" } } });
    // @ts-expect-error caddy names the directory it imports, or a preset
    vpsProvider({ ssh: "prod", proxy: { caddy: { container: "caddy" } } });
  });

  it("offers each proxy only the presets that run it", () => {
    // @ts-expect-error dokploy runs a traefik, never a caddy
    vpsProvider({ ssh: "prod", proxy: { caddy: { preset: "dokploy" } } });
    vpsProvider({
      ssh: "prod",
      proxy: {
        // @ts-expect-error caprover is no host tool with a preset, even beside every field spelled out
        traefik: { preset: "caprover", directory: "/traefik/dynamic", resolver: "letsencrypt" },
      },
    });
  });
});

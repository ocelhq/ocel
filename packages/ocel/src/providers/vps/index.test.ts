import { describe, expect, it } from "vitest";
import { defineConfig } from "../../config.js";
import vpsProvider from "./index";

describe("vpsProvider", () => {
  it("returns its options keyed by the provider, passing an ssh_config alias through", () => {
    expect(vpsProvider({ ssh: "prod-box" })).toEqual({
      vps: { ssh: "prod-box" },
    });
  });

  it("passes a spelled-out destination through to the provider", () => {
    expect(
      vpsProvider({
        ssh: {
          host: "203.0.113.10",
          port: 2222,
          user: "deploy",
          identityFile: "~/.ssh/id_ed25519",
        },
      }),
    ).toEqual({
      vps: {
        ssh: {
          host: "203.0.113.10",
          port: 2222,
          user: "deploy",
          identityFile: "~/.ssh/id_ed25519",
        },
      },
    });
  });

  it("passes the public key the deploy login is to answer to", () => {
    expect(vpsProvider({ ssh: "prod-box", deployKey: "~/.ssh/ocel-deploy.pub" })).toEqual({
      vps: { ssh: "prod-box", deployKey: "~/.ssh/ocel-deploy.pub" },
    });
  });

  it("serializes an unauthored port away rather than to a zero", () => {
    const config = defineConfig({
      slug: "test-app",
      provider: vpsProvider({ ssh: { host: "203.0.113.10" } }),
    });

    expect(JSON.parse(JSON.stringify(config.provider))).toEqual({
      vps: { ssh: { host: "203.0.113.10" } },
    });
  });

  it("type-checks as a `provider` field and serializes to its options keyed by vps", () => {
    const config = defineConfig({
      slug: "test-app",
      provider: vpsProvider({ ssh: { host: "203.0.113.10", user: "deploy" } }),
    });

    expect(JSON.parse(JSON.stringify(config.provider))).toEqual({
      vps: { ssh: { host: "203.0.113.10", user: "deploy" } },
    });
  });
});

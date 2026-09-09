import { describe, expect, it } from "vitest";
import { defineConfig } from "../../config.js";
import vpsProvider from "./index";

describe("vpsProvider", () => {
  it("returns a descriptor naming the provider, carrying an ssh_config alias through", () => {
    expect(vpsProvider({ ssh: "prod-box" })).toEqual({
      name: "vps",
      options: { ssh: "prod-box" },
    });
  });

  it("carries a spelled-out destination through to the provider", () => {
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
      name: "vps",
      options: {
        ssh: {
          host: "203.0.113.10",
          port: 2222,
          user: "deploy",
          identityFile: "~/.ssh/id_ed25519",
        },
      },
    });
  });

  it("carries the public key the deploy login is to answer to", () => {
    expect(vpsProvider({ ssh: "prod-box", deployKey: "~/.ssh/ocel-deploy.pub" })).toEqual({
      name: "vps",
      options: { ssh: "prod-box", deployKey: "~/.ssh/ocel-deploy.pub" },
    });
  });

  it("serializes an unauthored port away rather than to a zero", () => {
    const config = defineConfig({
      slug: "test-app",
      provider: vpsProvider({ ssh: { host: "203.0.113.10" } }),
    });

    expect(JSON.parse(JSON.stringify(config.provider))).toEqual({
      name: "vps",
      options: { ssh: { host: "203.0.113.10" } },
    });
  });

  it("type-checks as a `provider` field and serializes to { name, options }", () => {
    const config = defineConfig({
      slug: "test-app",
      provider: vpsProvider({ ssh: { host: "203.0.113.10", user: "deploy" } }),
    });

    expect(JSON.parse(JSON.stringify(config.provider))).toEqual({
      name: "vps",
      options: { ssh: { host: "203.0.113.10", user: "deploy" } },
    });
  });
});

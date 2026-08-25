import { defineConfig } from "ocel/config";
import { describe, expect, it } from "vitest";
import vpsProvider from "./index";

describe("vpsProvider", () => {
  it("returns a descriptor naming this package, carrying an ssh_config alias through", () => {
    expect(vpsProvider({ ssh: "prod-box" })).toEqual({
      package: "@ocel/provider-vps",
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
      package: "@ocel/provider-vps",
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

  it("serializes an unauthored port away rather than to a zero", () => {
    const config = defineConfig({
      slug: "test-app",
      provider: vpsProvider({ ssh: { host: "203.0.113.10" } }),
    });

    expect(JSON.parse(JSON.stringify(config.provider))).toEqual({
      package: "@ocel/provider-vps",
      options: { ssh: { host: "203.0.113.10" } },
    });
  });

  it("type-checks as an ocel.config.ts `provider` field and serializes to { package, options }", () => {
    const config = defineConfig({
      slug: "test-app",
      provider: vpsProvider({ ssh: { host: "203.0.113.10", user: "deploy" } }),
    });

    expect(JSON.parse(JSON.stringify(config.provider))).toEqual({
      package: "@ocel/provider-vps",
      options: { ssh: { host: "203.0.113.10", user: "deploy" } },
    });
  });
});

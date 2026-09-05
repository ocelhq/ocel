import { describe, expect, it } from "bun:test";
import {
  apiGateway,
  cloudflare,
  compose,
  container,
  hello,
  helloApiGateway,
  runsOn,
  SUPPRESS_RESOURCES_ENV,
} from "./variants";

describe("the variants the catalogue offers", () => {
  it("runs hello on every target that can suppress resources, and the aws ones on aws alone", () => {
    expect(runsOn(hello, "aws")).toBe(true);
    expect(runsOn(hello, "vps")).toBe(true);
    expect(runsOn(hello, "dev")).toBe(false);
    for (const one of [container, apiGateway, cloudflare]) {
      expect(runsOn(one, "aws")).toBe(true);
      expect(runsOn(one, "vps")).toBe(false);
    }
  });

  it("alters the app's environment, its config, or the suites it keeps, and nothing else", () => {
    expect(hello.env).toEqual({ [SUPPRESS_RESOURCES_ENV]: "1" });
    expect(hello.suites).toEqual(["health", "static"]);
    expect(hello.config).toBeUndefined();
    expect(container.config).toEqual({ compute: "container" });
    expect(apiGateway.config).toEqual({ edge: "api-gateway" });
    expect(cloudflare.config).toEqual({ edge: "cloudflare" });
  });
});

describe("composing two variants", () => {
  it("joins the names, merges env and config, and keeps the suites and targets both keep", () => {
    expect(helloApiGateway).toEqual({
      name: "hello-api-gateway",
      on: ["aws"],
      suites: ["health", "static"],
      env: { [SUPPRESS_RESOURCES_ENV]: "1" },
      config: { edge: "api-gateway" },
    });
  });

  it("lets the later variant's config win where both name one key", () => {
    expect(compose(apiGateway, cloudflare).config).toEqual({ edge: "cloudflare" });
  });

  it("runs everywhere both run when neither names a target", () => {
    const plain = compose(
      { name: "one", env: { A: "1" } },
      { name: "two", env: { B: "2" } },
    );
    expect(plain).toEqual({ name: "one-two", env: { A: "1", B: "2" } });
  });
});

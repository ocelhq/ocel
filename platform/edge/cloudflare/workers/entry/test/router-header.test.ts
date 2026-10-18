import { describe, expect, it } from "vitest";
import { resolveServe, type ServeFetch } from "../src/index";
import type { PointerRecordResult, ReleasesBinding, StoredRecord } from "../src/releases";
import { servedNextRelease } from "./next-release";
import { answerEveryRecordWith } from "./origin-deps";
import { resolveBase } from "./resolve-base";

const FN_URL = "https://abc123.lambda-url.eu-west-2.on.aws/";

const originRecord: StoredRecord = {
  app: "api",
  framework: "node",
  buildId: "0123456789abcdef",
  rootFunction: "api",
  functionUrls: { api: FN_URL },
  assetPrefix: "",
  isrPrefix: "",
  createdAt: 1_000,
  edge: {},
};

function bindingReturning(result: PointerRecordResult): ReleasesBinding {
  return answerEveryRecordWith(async () => {
    return result;
  });
}

async function resolved(binding: ReleasesBinding, host: string) {
  return resolveServe(
    { binding, slug: "p1", host, app: "api" },
    resolveBase({ fetch: (async () => new Response("origin")) as typeof fetch }),
  );
}

describe("the Cloudflare router names itself on every response", () => {
  it("marks the bootstrap placeholder a bound host lands on", async () => {
    const served = await resolved(
      bindingReturning({ kind: "no-pointer" }),
      "bootstrap.example.com",
    );

    expect(served).toBeInstanceOf(Response);
    expect((served as Response).status).toBe(404);
    expect((served as Response).headers.get("x-ocel-router")).toBe("cloudflare");
  });

  it("marks the response of a store that cannot answer", async () => {
    const served = await resolved(
      bindingReturning({ kind: "unchanged", release: "b1" }),
      "unchanged.example.com",
    );

    expect(served).toBeInstanceOf(Response);
    expect((served as Response).status).toBe(503);
    expect((served as Response).headers.get("x-ocel-router")).toBe("cloudflare");
  });

  it("marks what dispatch serves", async () => {
    const serve = servedNextRelease({
      table: { dispatch: { "/a": { kind: "static" } } },
      assets: { "/a.html": "<h1>a</h1>" },
    });

    const response = await serve(new Request("https://shop.example.com/a"));

    expect(response.status).toBe(200);
    expect(response.headers.get("x-ocel-router")).toBe("cloudflare");
  });

  it("marks what an origin-served deployment answers", async () => {
    const served = await resolved(
      bindingReturning({ kind: "record", release: "b1", record: originRecord }),
      "origin.example.com",
    );

    expect(typeof served).toBe("function");
    const response = await (served as ServeFetch)(new Request("https://origin.example.com/"));
    expect(response.headers.get("x-ocel-router")).toBe("cloudflare");
  });
});

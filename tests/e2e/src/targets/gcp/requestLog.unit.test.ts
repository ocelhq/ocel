import { afterEach, describe, expect, it } from "bun:test";
import { countOriginRequests, originRequestFilter } from "./requestLog";

describe("the filter for the requests Cloud Run logged", () => {
  it("selects Cloud Run request logs whose url holds the fragment, and nothing else", () => {
    expect(originRequestFilter("e2e-static=1234")).toBe(
      'resource.type="cloud_run_revision" AND log_id("run.googleapis.com/requests") AND httpRequest.requestUrl:"e2e-static=1234"',
    );
  });

  it("cannot be widened by a quote in the fragment", () => {
    expect(originRequestFilter('a" OR "b')).toContain('requestUrl:"a\\" OR \\"b"');
  });
});

const original = globalThis.fetch;

afterEach(() => {
  globalThis.fetch = original;
});

describe("counting the requests Cloud Run logged", () => {
  it("counts the entries Cloud Logging lists for the project", async () => {
    let asked: { url: string; body: string } | undefined;
    globalThis.fetch = (async (url: string, init: RequestInit) => {
      asked = { url, body: String(init.body) };
      return new Response(JSON.stringify({ entries: [{}, {}] }), { status: 200 });
    }) as typeof fetch;

    const counted = await countOriginRequests(
      { endpoint: undefined, project: "acme", region: "europe-west1", token: "t" },
      "e2e=1",
    );

    expect(counted).toBe(2);
    expect(asked?.url).toBe("https://logging.googleapis.com/v2/entries:list");
    expect(JSON.parse(asked?.body ?? "{}").resourceNames).toEqual(["projects/acme"]);
  });

  it("counts none when Cloud Logging lists no entries", async () => {
    globalThis.fetch = (async () => new Response("{}", { status: 200 })) as typeof fetch;

    const counted = await countOriginRequests(
      { endpoint: undefined, project: "acme", region: "europe-west1", token: "t" },
      "e2e=1",
    );

    expect(counted).toBe(0);
  });

  it("refuses to answer against an emulator that keeps no logs", async () => {
    await expect(
      countOriginRequests(
        { endpoint: "http://127.0.0.1:1", project: "floci-local", region: "r", token: undefined },
        "e2e=1",
      ),
    ).rejects.toThrow(/not emulated/);
  });
});

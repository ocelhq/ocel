import { describe, expect, it } from "bun:test";
import type { CheckContext, Fetch } from "./context";
import { revisionTagChecks } from "./revisionTag";

const BASE = "https://web-j-1-deploy-node.journey.test";
const TAGS = [
  "https://r2b3c4d5e---ocel-prod-web-abc123-ew.a.run.app",
  "https://r1a2b3c4d---ocel-prod-web-abc123-ew.a.run.app",
];

function context(fetch: Fetch, tags: string[] = TAGS): CheckContext {
  return {
    app: "web",
    baseUrl: BASE,
    greeting: "journey-hello",
    maxRequestBodyBytes: 1024,
    phase: "deploy",
    notes: new Map(),
    fetch,
    reach: async (url) => url,
    readExposed: async () => "",
    countOriginRequests: async () => 0,
    readReleaseFile: async () => new Uint8Array(),
    readRevisionTagUrls: async () => tags,
    runInEnvironment: async () => "",
    journeyNonce: "journey-nonce",
    projectDir: "/nowhere",
    tempDir: "/nowhere",
  };
}

type Asked = { url: string; secret: string | null };

function answering(answer: (asked: Asked) => number): { fetch: Fetch; asked: Asked[] } {
  const asked: Asked[] = [];
  const fetch: Fetch = async (input, init) => {
    const one = {
      url: input instanceof Request ? input.url : String(input),
      secret: new Headers(init?.headers).get("x-ocel-origin-secret"),
    };
    asked.push(one);
    return new Response("", { status: answer(one) });
  };
  return { fetch, asked };
}

const edgeOnly = ({ url }: Asked) => (url.startsWith(BASE) ? 200 : 403);

function failure(work: Promise<unknown>): Promise<string> {
  return work.then(
    () => "",
    (error: unknown) => (error as Error).message,
  );
}

async function ran(fetch: Fetch, tags?: string[]): Promise<string> {
  const [check] = revisionTagChecks;
  if (!check) {
    throw new Error("no revision-tag check");
  }
  return failure(check.run(context(fetch, tags)));
}

describe("the revision-tag check", () => {
  it("passes when every tag refuses a request with no origin secret or a guessed one, and the edge answers", async () => {
    const { fetch, asked } = answering(edgeOnly);

    expect(await ran(fetch)).toBe("");
    for (const tag of TAGS) {
      expect(asked.filter((one) => one.url.startsWith(tag))).toEqual([
        { url: `${tag}/`, secret: null },
        { url: `${tag}/`, secret: expect.any(String) },
        { url: `${tag}/health`, secret: null },
        { url: `${tag}/health`, secret: expect.any(String) },
      ]);
    }
    expect(asked.some((one) => one.url.startsWith(BASE))).toBe(true);
  });

  it("fails when a tag answers a request that carries no origin secret", async () => {
    const { fetch } = answering(({ url, secret }) =>
      url.startsWith(TAGS[1] ?? "") && secret === null ? 200 : edgeOnly({ url, secret }),
    );

    expect(await ran(fetch)).toContain(TAGS[1]);
  });

  it("fails when a tag answers a request on its root, the health path Cloud Run probes, that carries no origin secret", async () => {
    const { fetch } = answering(({ url, secret }) =>
      url === `${TAGS[0]}/` && secret === null ? 200 : edgeOnly({ url, secret }),
    );

    expect(await ran(fetch)).toContain(`${TAGS[0]}/ `);
  });

  it("fails when a tag answers a request that carries a guessed origin secret", async () => {
    const { fetch } = answering(({ url, secret }) =>
      secret !== null ? 200 : edgeOnly({ url, secret }),
    );

    expect(await ran(fetch)).toContain("guess");
  });

  it("fails when the edge does not answer", async () => {
    const { fetch } = answering(() => 403);

    expect(await ran(fetch)).toContain(BASE);
  });

  it("fails when the app carries no tag, so nothing is reached around the edge", async () => {
    const { fetch } = answering(edgeOnly);

    expect(await ran(fetch, [])).toContain("no revision-tag URL");
  });
});

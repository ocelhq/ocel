import { describe, expect, it } from "bun:test";
import { type BucketTiming, backendBucketChecksWith, immutableFileOf } from "./backendBucket";
import type { CheckContext, Fetch } from "./context";

describe("the immutable file a page links", () => {
  it("is the first script or stylesheet under the immutable prefix", () => {
    const html =
      '<link rel="stylesheet" href="/_next/static/css/app.css?dpl=1"/><script src="/_next/static/chunks/main-abc.js" async></script>';
    expect(immutableFileOf(html)).toBe("/_next/static/css/app.css");
  });

  it("is none when the page links nothing under it", () => {
    expect(immutableFileOf('<script src="/other/main.js"></script>')).toBeUndefined();
  });
});

const INSTANT: BucketTiming = {
  propagationMs: 50,
  pollMs: 0,
  loggingMs: 0,
  settleMs: 0,
  strictAttempts: 3,
};

const UPLOADED = "console.log('this release')";

type World = {
  bodies: string[];
  logged: boolean[];
  asked: number;
};

function world(bodies: string[], logged: boolean[]): World {
  return { bodies, logged, asked: 0 };
}

function context(at: World): CheckContext {
  const fetch: Fetch = async (input) => {
    const url = new URL(String(input));
    if (url.pathname === "/") {
      return new Response('<script src="/_next/static/chunks/a.js"></script>', { status: 200 });
    }
    if (url.searchParams.has("e2e-dynamic")) {
      return new Response("", { status: 200 });
    }
    const turn = at.asked;
    at.asked += 1;
    if (url.pathname.includes("missing-")) {
      return new Response("", { status: 404 });
    }
    return new Response(at.bodies[Math.min(turn, at.bodies.length - 1)], {
      status: 200,
      headers: { "cache-control": "public, max-age=31536000, immutable" },
    });
  };
  const countOriginRequests = async (fragment: string) => {
    if (fragment.startsWith("e2e-dynamic=")) {
      return 1;
    }
    return at.logged[Math.min(at.asked - 1, at.logged.length - 1)] ? 1 : 0;
  };
  return {
    app: "web",
    baseUrl: "https://web.journey.test",
    greeting: "journey-hello",
    maxRequestBodyBytes: 1024,
    phase: "verify",
    notes: new Map(),
    fetch,
    reach: async (url) => url,
    readExposed: async () => "",
    countOriginRequests,
    readReleaseFile: async () => new TextEncoder().encode(UPLOADED),
    runInEnvironment: async () => "",
    journeyNonce: "journey-nonce",
    projectDir: "/nowhere",
    tempDir: "/nowhere",
  };
}

describe("the backend bucket checks", () => {
  const [servesImmutable, missesImmutable] = backendBucketChecksWith(INSTANT);

  it("pass when every request for the release's file is answered with its bytes and only the control reaches Cloud Run", async () => {
    await expect(
      servesImmutable?.run(context(world([UPLOADED], [false]))),
    ).resolves.toBeUndefined();
  });

  it("wait while the url map still sends the file to Cloud Run, then hold it to the bucket", async () => {
    const settling = world([UPLOADED], [true, true, false]);

    await expect(servesImmutable?.run(context(settling))).resolves.toBeUndefined();
    expect(settling.asked).toBe(3 + INSTANT.strictAttempts);
  });

  it("fail when Cloud Run logs a request for the file after one was served from the bucket", async () => {
    await expect(
      servesImmutable?.run(context(world([UPLOADED], [false, false, true]))),
    ).rejects.toThrow(/Cloud Run logged/);
  });

  it("fail when the file served is not the one the current release uploaded", async () => {
    await expect(
      servesImmutable?.run(context(world(["console.log('an older release')"], [false]))),
    ).rejects.toThrow(/not the bytes the current release uploaded/);
  });

  it("pass when a missing file is a 404 Cloud Run never logs", async () => {
    await expect(
      missesImmutable?.run(context(world([UPLOADED], [false]))),
    ).resolves.toBeUndefined();
  });

  it("fail when Cloud Run logs a request for a missing file after the bucket answered one", async () => {
    await expect(missesImmutable?.run(context(world([UPLOADED], [false, true])))).rejects.toThrow(
      /Cloud Run logged/,
    );
  });
});

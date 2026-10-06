import { expect, test } from "vitest";
import { newMetadataToken } from "../src/next/metadata-token.mjs";

const path = "/computeMetadata/v1/instance/service-accounts/default/token";

function metadata(expiresIn = 3600) {
  const urls: string[] = [];
  let n = 0;
  const fetchStub = (async (input: string | URL | Request) => {
    urls.push(String(input));
    await Promise.resolve();
    return Response.json({ access_token: `t${++n}`, expires_in: expiresIn });
  }) as typeof fetch;
  return { fetch: fetchStub, urls };
}

test("the token is fetched once and reused until a minute before it expires", async () => {
  const m = metadata(3600);
  let clock = 0;
  const source = newMetadataToken({ fetch: m.fetch, now: () => clock, service: "Firestore" });

  expect(await source.token()).toBe("t1");
  clock = 3_539_000;
  expect(await source.token()).toBe("t1");
  clock = 3_540_000;
  expect(await source.token()).toBe("t2");
  expect(m.urls).toEqual([
    `http://metadata.google.internal${path}`,
    `http://metadata.google.internal${path}`,
  ]);
});

test("callers asking at once share one fetch", async () => {
  const m = metadata();
  const source = newMetadataToken({ fetch: m.fetch, service: "Firestore" });

  const tokens = await Promise.all([source.token(), source.token(), source.token()]);

  expect(tokens).toEqual(["t1", "t1", "t1"]);
  expect(m.urls).toHaveLength(1);
});

test("a token the service rejected is fetched again", async () => {
  const m = metadata();
  const source = newMetadataToken({ fetch: m.fetch, service: "Firestore" });

  await source.token();
  source.forget();

  expect(await source.token()).toBe("t2");
});

test("a metadata server that gives no token names the service", async () => {
  const source = newMetadataToken({
    fetch: (async () => new Response("", { status: 503 })) as typeof fetch,
    service: "Firestore",
  });

  await expect(source.token()).rejects.toThrow(
    "ocel: the metadata server gave no Firestore token: 503",
  );
});

test("a metadata server that never answers fails the token after its timeout", async () => {
  const hang = ((_input: string | URL | Request, init?: RequestInit) =>
    new Promise<Response>((_, reject) => {
      init?.signal?.addEventListener("abort", () => reject(new Error("aborted")));
    })) as typeof fetch;
  const source = newMetadataToken({ fetch: hang, service: "Firestore", timeoutMs: 20 });

  await expect(source.token()).rejects.toThrow("aborted");
});

test("a body that stalls after the metadata server answered fails the token after its timeout", async () => {
  const stalled = (async (_input: string | URL | Request, init?: RequestInit) => ({
    ok: true,
    json: () =>
      new Promise<never>((_, reject) => {
        init?.signal?.addEventListener("abort", () => reject(new Error("aborted")));
      }),
  })) as unknown as typeof fetch;
  const source = newMetadataToken({ fetch: stalled, service: "Firestore", timeoutMs: 20 });

  await expect(source.token()).rejects.toThrow("aborted");
});

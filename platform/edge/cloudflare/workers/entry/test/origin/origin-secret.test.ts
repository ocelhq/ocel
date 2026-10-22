import { describe, expect, it } from "vitest";
import { originSecretFetch, presentedSecret } from "../../src/origin/origin-secret";
import { capturing, withGlobalFetch } from "../origin-deps";

const ORIGIN_SECRET = "x-ocel-origin-secret";
const CLIENT_AUTHORIZATION = "x-ocel-client-authorization";
const REVISION_URL = "https://r9a0c0e00---web-abc123-ew.a.run.app/x";

const CURRENT_FINGERPRINT = "97b0560280ed60a5";
const PREVIOUS_FINGERPRINT = "6da0633528deaa01";

describe("the secret presented to a release", () => {
  const rotating = {
    secret: "current",
    previous: { secret: "previous", fingerprint: PREVIOUS_FINGERPRINT },
  };

  it("is the previous secret for a release that holds the one the rotation replaced", () => {
    expect(presentedSecret(rotating, PREVIOUS_FINGERPRINT)).toBe("previous");
  });

  it("is the current secret for a release that holds it", () => {
    expect(presentedSecret(rotating, CURRENT_FINGERPRINT)).toBe("current");
  });

  it("is the current secret for a release that names a secret the worker holds neither of", () => {
    expect(presentedSecret(rotating, "0000000000000000")).toBe("current");
  });

  it("is the current secret for a release that names none", () => {
    expect(presentedSecret(rotating, undefined)).toBe("current");
  });

  it("is the current secret to every release when no rotation is under way", () => {
    expect(presentedSecret({ secret: "current" }, PREVIOUS_FINGERPRINT)).toBe("current");
    expect(presentedSecret({ secret: "current" }, undefined)).toBe("current");
  });
});

describe("originSecretFetch", () => {
  it("presents the secret to the origin", async () => {
    const wire = capturing();

    await withGlobalFetch(wire.fetch, () =>
      originSecretFetch("current")(new Request(REVISION_URL)),
    );

    expect(wire.calls).toHaveLength(1);
    expect(wire.calls[0].url).toBe(REVISION_URL);
    expect(wire.calls[0].headers.get(ORIGIN_SECRET)).toBe("current");
    expect(wire.calls[0].redirect).toBe("manual");
  });

  it("replaces an origin secret the visitor sent with its own", async () => {
    const wire = capturing();

    await withGlobalFetch(wire.fetch, () =>
      originSecretFetch("current")(
        new Request(REVISION_URL, { headers: { [ORIGIN_SECRET]: "a-guess" } }),
      ),
    );

    expect(wire.calls[0].headers.get(ORIGIN_SECRET)).toBe("current");
  });

  it("passes the visitor's authorization through and drops a forged client-authorization carrier", async () => {
    const wire = capturing();

    await withGlobalFetch(wire.fetch, () =>
      originSecretFetch("current")(
        new Request(REVISION_URL, {
          headers: { authorization: "Bearer t", [CLIENT_AUTHORIZATION]: "forged" },
        }),
      ),
    );

    expect(wire.calls[0].headers.get("authorization")).toBe("Bearer t");
    expect(wire.calls[0].headers.has(CLIENT_AUTHORIZATION)).toBe(false);
  });

  it("passes a request body through unchanged", async () => {
    const wire = capturing();

    await withGlobalFetch(wire.fetch, () =>
      originSecretFetch("current")(
        new Request(REVISION_URL, { method: "POST", body: new Uint8Array([1, 2, 3, 0, 255]) }),
      ),
    );

    expect(wire.calls[0].method).toBe("POST");
    expect([...new Uint8Array(await wire.calls[0].arrayBuffer())]).toEqual([1, 2, 3, 0, 255]);
  });

  it("drops the empty-body sentinel", async () => {
    const answer = (async () =>
      new Response("x", { headers: { "x-ocel-empty-body": "1" } })) as typeof fetch;

    const response = await withGlobalFetch(answer, () =>
      originSecretFetch("current")(new Request(REVISION_URL)),
    );

    expect(response.headers.has("x-ocel-empty-body")).toBe(false);
    expect(await response.text()).toBe("");
  });

  it("refuses to present the secret to a non-https origin", async () => {
    const wire = capturing();

    await expect(
      withGlobalFetch(wire.fetch, () =>
        originSecretFetch("current")(new Request("http://web-abc123-ew.a.run.app/x")),
      ),
    ).rejects.toThrow("ocel: refusing to present the origin secret over http:");
    expect(wire.calls).toHaveLength(0);
  });
});

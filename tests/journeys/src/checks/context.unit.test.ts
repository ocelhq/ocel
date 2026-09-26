import { describe, expect, it } from "bun:test";
import { type CheckContext, json, REDACTED, redact, SECRET_TOKEN, secretsOf } from "./context";
import { healthChecks } from "./health";

function answering(app: string): CheckContext["fetch"] {
  return async () =>
    new Response(JSON.stringify({ ok: true, app }), {
      status: 200,
      headers: { "content-type": "application/json" },
    });
}

function context(asked: string, answered: string): CheckContext {
  return {
    app: asked,
    baseUrl: `https://${asked}-j-1-sdk-workspace.journey.test`,
    greeting: "journey-hello",
    maxRequestBodyBytes: 1024,
    phase: "verify",
    notes: new Map(),
    fetch: answering(answered),
  };
}

const health = healthChecks[0];

describe("the health check", () => {
  it("passes when the hostname answers with the app it was asked for", async () => {
    expect(health).toBeDefined();
    await expect(health?.run(context("node", "node"))).resolves.toBeUndefined();
  });

  it("fails when a hostname answers with another app of the same project", async () => {
    await expect(health?.run(context("node", "next"))).rejects.toThrow(/next/);
  });
});

describe("json", () => {
  it("describes the response when the body is not JSON at all", async () => {
    const ctx: CheckContext = {
      ...context("node", "node"),
      fetch: async () =>
        new Response("<html>bad gateway</html>", {
          status: 502,
          headers: { "content-type": "text/html" },
        }),
    };

    const failure = await json(ctx, "/health").then(
      () => undefined,
      (error: unknown) => (error as Error).message,
    );

    expect(failure).toContain("/health did not answer JSON");
    expect(failure).toContain("status 502");
    expect(failure).toContain("content-type: text/html");
    expect(failure).toContain("bad gateway");
  });
});

describe("redact", () => {
  it("masks each secret it is handed, wherever a log or evidence contains it", () => {
    expect(
      redact("login ghs_s3cret ok, again ghs_s3cret as hunter2", ["ghs_s3cret", "hunter2"]),
    ).toBe(`login ${REDACTED} ok, again ${REDACTED} as ${REDACTED}`);
  });

  it("masks a password written into a url or a json document it was never handed", () => {
    expect(redact('https://u:hunter2@host {"password": "hunter2"}', [])).toBe(
      `https://u:${REDACTED}@host {"password": "${REDACTED}"}`,
    );
  });
});

describe("secretsOf", () => {
  it("lists the registry token among the journey's secrets when the run has one", () => {
    expect(secretsOf({ OCEL_JOURNEY_REGISTRY_TOKEN: "ghs_s3cret" })).toEqual([
      SECRET_TOKEN,
      "ghs_s3cret",
    ]);
  });

  it("lists only the journey's own secret when the run has no registry token", () => {
    expect(secretsOf({})).toEqual([SECRET_TOKEN]);
    expect(secretsOf({ OCEL_JOURNEY_REGISTRY_TOKEN: " " })).toEqual([SECRET_TOKEN]);
  });
});

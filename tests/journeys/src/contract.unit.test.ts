import { describe, expect, it } from "bun:test";
import { json, type ContractContext } from "./contract";
import { healthRows } from "./rows";

function answering(app: string): ContractContext["fetch"] {
  return async () =>
    new Response(JSON.stringify({ ok: true, app }), {
      status: 200,
      headers: { "content-type": "application/json" },
    });
}

function context(asked: string, answered: string): ContractContext {
  return {
    app: asked,
    baseUrl: `https://${asked}-j-1-sdk-workspace.journey.test`,
    greeting: "journey-hello",
    leg: "contract",
    notes: new Map(),
    fetch: answering(answered),
  };
}

const health = healthRows[0];

describe("the health row", () => {
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
    const ctx: ContractContext = {
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

import { describe, expect, it } from "vitest";
import { contract, type ContractContext } from "./contract";

const HEALTH = "GET /health answers with the app name";

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
    baseUrl: `https://${asked}.j-1-workspace.journey.test`,
    greeting: "journey-hello",
    leg: "contract",
    notes: new Map(),
    fetch: answering(answered),
  };
}

const health = contract.find((row) => row.title === HEALTH);

describe("the health row", () => {
  it("passes when the hostname answers with the app it was asked for", async () => {
    expect(health).toBeDefined();
    await expect(health?.run(context("express", "express"))).resolves.toBeUndefined();
  });

  it("fails when a hostname answers with another app of the same project", async () => {
    await expect(health?.run(context("express", "hono"))).rejects.toThrow(/hono/);
  });
});

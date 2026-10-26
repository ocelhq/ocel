import assert from "node:assert/strict";
import { type Check, json } from "./context";

export const bindingCheck: Check = {
  title: "GET /api/binding answers with what it resolved and the greeting it deployed with",
  run: async (ctx) => {
    const { res, body } = await json(ctx, "/api/binding");
    assert.equal(res.status, 200);
    const binding = body as {
      host: string;
      port: number;
      database: string;
      hasPassword: boolean;
      greeting: string;
    };
    assert.ok(binding.host.length > 0, "the app resolved no host");
    assert.equal(typeof binding.port, "number");
    assert.ok(binding.database.length > 0, "the app resolved no database");
    assert.equal(binding.hasPassword, true);
    assert.equal(binding.greeting, ctx.greeting);
  },
};

export const bindingQueryCheck: Check = {
  title: "GET /api/binding/query answers ok after a select through the binding",
  run: async (ctx) => {
    const { res, body } = await json(ctx, "/api/binding/query");
    assert.equal(res.status, 200);
    assert.deepEqual(body, { ok: true });
  },
};

export const bindingChecks: Check[] = [bindingCheck, bindingQueryCheck];

export const publishedVariablesCheck: Check = {
  title:
    "GET /api/variables answers with the plain and the secret variable the IaC program published",
  run: async (ctx) => {
    const binding = (await json(ctx, "/api/binding")).body as { port: number };
    const { res, body } = await json(ctx, "/api/variables");
    assert.equal(res.status, 200);
    const variables = body as { databasePort: string; apiKeyIsPublished: boolean };
    assert.equal(variables.databasePort, String(binding.port));
    assert.equal(variables.apiKeyIsPublished, true, "the app read no published API_KEY");
  },
};

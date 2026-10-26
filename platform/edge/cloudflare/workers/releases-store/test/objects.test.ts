import { env, SELF } from "cloudflare:test";
import { describe, expect, it } from "vitest";
import type { Env } from "../src/env";

declare module "cloudflare:test" {
  interface ProvidedEnv extends Env {}
}

const BOOTSTRAP = "dev-secret";

function objectsForm(objects: Record<string, string>): FormData {
  const form = new FormData();
  for (const [key, body] of Object.entries(objects)) {
    form.append(key, new File([body], key, { type: "text/javascript" }));
  }
  return form;
}

function putObjects(objects: Record<string, string>, token = BOOTSTRAP) {
  return SELF.fetch(
    new Request("https://store.example/acme-web/objects", {
      method: "POST",
      headers: { authorization: `Bearer ${token}` },
      body: objectsForm(objects),
    }),
  );
}

function removeObjects(prefixes: string[], token = BOOTSTRAP) {
  return SELF.fetch(
    new Request("https://store.example/acme-web/objects/remove", {
      method: "POST",
      headers: { authorization: `Bearer ${token}` },
      body: JSON.stringify({ prefixes }),
    }),
  );
}

const asset = "assets/acme-web/prod/web/r00000b01/_next/static/app.js";

describe("the objects the store keeps for an origin with none of its own", () => {
  it("keeps each object put with the store's own credential under its key", async () => {
    const res = await putObjects({ [asset]: "chunk" });

    expect(res.status).toBe(204);
    const kept = await env.OBJECTS?.get(asset);
    expect(await kept?.text()).toBe("chunk");
    expect(kept?.httpMetadata?.contentType).toBe("text/javascript");
  });

  it("refuses a put by anyone holding no bootstrap credential", async () => {
    const res = await putObjects({ [asset]: "chunk" }, "project-secret");

    expect(res.status).toBe(401);
  });

  it("refuses a key that lies outside a release's storage prefix", async () => {
    const res = await putObjects({ "../../elsewhere": "x" });

    expect(res.status).toBe(400);
  });

  it("removes every object under the prefixes named", async () => {
    const other = "assets/acme-web/prod/web/r00000b02/app.js";
    await putObjects({ [asset]: "chunk", [other]: "kept" });

    const res = await removeObjects(["assets/acme-web/prod/web/r00000b01/"]);

    expect(res.status).toBe(204);
    expect(await env.OBJECTS?.get(asset)).toBeNull();
    expect(await (await env.OBJECTS?.get(other))?.text()).toBe("kept");
  });

  it("refuses to remove a prefix naming no release's objects", async () => {
    const res = await removeObjects([""]);

    expect(res.status).toBe(400);
  });
});

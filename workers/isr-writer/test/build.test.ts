import { env, runInDurableObject } from "cloudflare:test";
import { describe, expect, it } from "vitest";

import { claimBuild, claimedBuild } from "../src/build";

// Exercised against a real Durable Object's storage rather than a fake, so the
// claim that survives eviction is the one that ships.
async function withStorage<T>(name: string, fn: (store: DurableObjectStorage) => T): Promise<T> {
  const id = env.ISR_SNAPSHOT_DO.idFromName(name);
  return runInDurableObject(env.ISR_SNAPSHOT_DO.get(id), (_instance, ctx) => fn(ctx.storage));
}

describe("build claim", () => {
  // An object woken by an alarm is handed nothing, and by then its ctx.id no
  // longer carries the name it was addressed by.
  it("reads back nothing before an object has published for a build", async () => {
    expect(await withStorage("prod/p/web/UNCLAIMED", claimedBuild)).toBeUndefined();
  });

  it("reads back the build an object claimed", async () => {
    await withStorage("prod/p/web/B1", (store) => claimBuild(store, "prod/p/web/B1"));
    expect(await withStorage("prod/p/web/B1", claimedBuild)).toBe("prod/p/web/B1");
  });
});

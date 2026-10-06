import { createHash, randomBytes } from "node:crypto";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { type Firestore, newFirestore } from "../../src/next/firestore.mjs";
import { newFirestoreTagRecords } from "../../src/next/tag-records.mjs";
import { type Backend, openBackend } from "./backend.mjs";

const maxDeletesPerCommit = 500;

describe("tag records against a live backend", () => {
  let backend: Backend;
  let firestore: Firestore;
  const written: { isrPrefix: string; tag: string }[] = [];

  const prefixOf = () => `live/${randomBytes(6).toString("hex")}/isr`;

  const instanceOn = (isrPrefix: string, tags: string[]) => {
    for (const tag of tags) written.push({ isrPrefix, tag });
    return newFirestoreTagRecords(firestore, isrPrefix);
  };

  beforeAll(async () => {
    backend = await openBackend();
    firestore = newFirestore({
      database: backend.tagDatabase(),
      endpoint: backend.firestoreEndpoint,
      metadataOrigin: backend.metadataOrigin,
    });
  });

  afterAll(async () => {
    if (backend === undefined) return;
    if (firestore !== undefined) {
      const names = written.map(({ isrPrefix, tag }) => {
        const id = createHash("sha256").update(`${isrPrefix}/${tag}`).digest("hex");
        return `${firestore.database}/documents/tags/${id}`;
      });
      for (let from = 0; from < names.length; from += maxDeletesPerCommit) {
        await firestore.commit(
          names.slice(from, from + maxDeletesPerCommit).map((name) => ({ delete: name })),
        );
      }
    }
    await backend.close();
  });

  it("reads on one instance a tag another instance published", async () => {
    const isrPrefix = prefixOf();
    const a = instanceOn(isrPrefix, ["products"]);
    const b = instanceOn(isrPrefix, []);
    const expired = Date.now();

    await a.publish("products", { expired });
    const read = await b.read(null);

    expect(read.status).toBe("fresh");
    if (read.status !== "fresh") return;
    expect(read.records.products?.expired).toBe(expired);
  });

  it("keeps the larger time when a later publish carries a smaller one", async () => {
    const isrPrefix = prefixOf();
    const tags = instanceOn(isrPrefix, ["products"]);

    await tags.publish("products", { expired: 5 });
    await tags.publish("products", { expired: 3 });
    const read = await tags.read(null);

    expect(read.status).toBe("fresh");
    if (read.status !== "fresh") return;
    expect(read.records.products?.expired).toBe(5);
  });

  it("answers from a cursor only what was written after it", async () => {
    const isrPrefix = prefixOf();
    const tags = instanceOn(isrPrefix, ["products", "late"]);

    await tags.publish("products", { expired: 1 });
    await new Promise((resolve) => setTimeout(resolve, 2500));
    const first = await tags.read(null);
    if (first.status !== "fresh") throw new Error("the first read was not fresh");
    await tags.publish("late", { expired: 2 });
    const second = await tags.read(first.cursor);

    expect(second.status).toBe("fresh");
    if (second.status !== "fresh") return;
    expect(Object.keys(second.records)).toContain("late");
    expect(Object.keys(second.records)).not.toContain("products");
  });

  it("never reads the tags of another deployment's prefix", async () => {
    const mine = instanceOn(prefixOf(), ["products"]);
    const theirs = instanceOn(prefixOf(), []);

    await mine.publish("products", { expired: 1 });
    const read = await theirs.read(null);

    expect(read.status).toBe("fresh");
    if (read.status !== "fresh") return;
    expect(read.records).toEqual({});
  });
});

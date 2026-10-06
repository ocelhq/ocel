import { randomBytes } from "node:crypto";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { type CloudStorage, newCloudStorage } from "../../src/next/cloud-storage.mjs";
import { type Backend, openBackend } from "./backend.mjs";

describe("cloud storage against a live backend", () => {
  let backend: Backend;
  let storage: CloudStorage;
  let origin: string;
  let bucket: string;
  const written: string[] = [];

  const objectName = (label: string) => {
    const name = `live/${label}-${randomBytes(4).toString("hex")}`;
    written.push(name);
    return name;
  };

  beforeAll(async () => {
    backend = await openBackend();
    origin = backend.storageEndpoint ?? "https://storage.googleapis.com";
    bucket = `ocel-live-${randomBytes(5).toString("hex")}`;
    const created = await backend.authorizedFetch(
      `${origin}/storage/v1/b?project=${backend.project}`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: bucket,
          ...(backend.storageEndpoint === undefined && {
            location: process.env.OCEL_GCP_LIVE_REGION || "europe-west1",
          }),
        }),
      },
    );
    expect(created.status, await created.clone().text()).toBe(200);
    storage = newCloudStorage({
      bucket,
      endpoint: backend.storageEndpoint,
      metadataOrigin: backend.metadataOrigin,
    });
  });

  afterAll(async () => {
    if (backend === undefined) return;
    for (const name of written) {
      await backend.authorizedFetch(
        `${origin}/storage/v1/b/${bucket}/o/${encodeURIComponent(name)}`,
        { method: "DELETE" },
      );
    }
    if (bucket !== undefined) {
      await backend.authorizedFetch(`${origin}/storage/v1/b/${bucket}`, { method: "DELETE" });
    }
    await backend.close();
  });

  it("creates an object with ifGenerationMatch 0, and loses a second create with 0", async () => {
    const name = objectName("create");

    const first = await storage.write(name, "one", { ifGenerationMatch: "0" });
    const second = await storage.write(name, "two", { ifGenerationMatch: "0" });

    expect(first.status).toBe("written");
    expect(second).toEqual({ status: "lost" });
  });

  it("reads back the body and the generation the write landed", async () => {
    const name = objectName("read");
    const wrote = await storage.write(name, "body", { ifGenerationMatch: "0" });
    if (wrote.status !== "written") throw new Error("the first create was lost");

    expect(await storage.read(name)).toEqual({
      status: "found",
      body: "body",
      generation: wrote.generation,
    });
  });

  it("lands a write conditioned on the current generation and loses one conditioned on the old one", async () => {
    const name = objectName("condition");
    const first = await storage.write(name, "b1", { ifGenerationMatch: "0" });
    if (first.status !== "written") throw new Error("the first create was lost");

    const second = await storage.write(name, "b2", { ifGenerationMatch: first.generation });
    if (second.status !== "written")
      throw new Error("the write on the current generation was lost");
    const stale = await storage.write(name, "b3", { ifGenerationMatch: first.generation });

    expect(second.generation).not.toBe(first.generation);
    expect(stale).toEqual({ status: "lost" });
    expect(await storage.read(name)).toEqual({
      status: "found",
      body: "b2",
      generation: second.generation,
    });
  });

  it("answers absent for an object nobody wrote", async () => {
    expect(await storage.read("live/nobody-wrote-this")).toEqual({ status: "absent" });
  });
});

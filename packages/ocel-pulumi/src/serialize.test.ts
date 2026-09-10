import { runtime } from "@pulumi/pulumi";
import { describe, expect, it } from "vitest";
import { postgresProvider } from "./resource.js";

describe("serialising the dynamic provider", () => {
  it("serialises the provider the way dynamic.Resource does", async () => {
    await expect(runtime.serializeFunction(() => postgresProvider)).resolves.toBeDefined();
  });
});

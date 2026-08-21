import { expect, it } from "vitest";
import { poll } from "../http";
import type { Check } from "../types";

export const checkRevalidate: Check = ({ baseUrl }) => {
  it("invalidates a tagged cache entry", async () => {
    const read = async () => {
      const response = await fetch(`${baseUrl()}/api/cache`);
      expect(response.status).toBe(200);
      return (await response.json()) as { producedAt: string };
    };
    const first = await read();
    expect((await read()).producedAt).toBe(first.producedAt);
    const revalidated = await fetch(
      `${baseUrl()}/api/revalidate?tag=example-cache`,
      { method: "POST" },
    );
    expect(revalidated.status).toBe(200);
    expect(await revalidated.json()).toEqual({ revalidated: "example-cache" });
    expect(
      await poll(
        async () => {
          const current = await read();
          return current.producedAt !== first.producedAt ? current : undefined;
        },
        { timeoutMs: 30_000, intervalMs: 500 },
      ),
    ).toBeDefined();
  }, 45_000);
};

import { expect, it } from "vitest";
import { poll } from "../http";
import type { Check } from "../types";

export const checkIsr: Check = ({ baseUrl, targetName }) => {
  it("revalidates the ISR probe", async () => {
    const readToken = async () => {
      const response = await fetch(`${baseUrl()}/isr`);
      expect(response.status).toBe(200);
      return {
        token: /isr-token:(?:<!--.*?-->)?(\d+)/.exec(await response.text())?.[1],
        tier: response.headers.get("x-ocel-cache"),
      };
    };
    const first = await readToken();
    expect(first.token).toBeDefined();
    const settled = await readToken();
    expect(settled.token).toBe(first.token);
    if (targetName === "aws") {
      expect(["HIT", "PRERENDER", "STALE"]).toContain(settled.tier);
    }
    const rewritten = await poll(
      async () => {
        const current = await readToken();
        return current.token !== first.token ? current : undefined;
      },
      { timeoutMs: 45_000, intervalMs: 1_000 },
    );
    expect(rewritten).toBeDefined();
    if (targetName === "aws") {
      expect(["HIT", "PRERENDER", "STALE"]).toContain(rewritten!.tier);
    }
  }, 60_000);
};

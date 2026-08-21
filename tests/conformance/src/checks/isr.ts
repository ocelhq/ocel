import { expect, it } from "vitest";
import { poll } from "../http";
import type { Check } from "../types";

export const checkIsr: Check = ({ baseUrl }) => {
  it("revalidates the ISR probe", async () => {
    const readToken = async () => {
      const response = await fetch(`${baseUrl()}/isr`);
      expect(response.status).toBe(200);
      return /isr-token:(?:<!--.*?-->)?(\d+)/.exec(await response.text())?.[1];
    };
    const first = await readToken();
    expect(first).toBeDefined();
    expect(await readToken()).toBe(first);
    expect(
      await poll(
        async () => {
          const current = await readToken();
          return current !== first ? current : undefined;
        },
        { timeoutMs: 45_000, intervalMs: 1_000 },
      ),
    ).toBeDefined();
  }, 60_000);
};

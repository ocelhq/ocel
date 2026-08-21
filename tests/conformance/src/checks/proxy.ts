import { expect, it } from "vitest";
import type { Check } from "../types";

export const checkProxy: Check = ({ baseUrl }) => {
  it("runs proxy rewrites, redirects, responses, and fall-through", async () => {
    const root = await fetch(baseUrl());
    expect(root.status).toBe(200);
    const rootBody = await root.text();

    const rewrite = await fetch(`${baseUrl()}/mw/rewrite`);
    expect(rewrite.status).toBe(200);
    expect(await rewrite.text()).toBe(rootBody);

    const redirect = await fetch(`${baseUrl()}/mw/redirect`, {
      redirect: "manual",
    });
    expect(redirect.status).toBeGreaterThanOrEqual(300);
    expect(redirect.status).toBeLessThan(400);
    expect(new URL(redirect.headers.get("location")!, baseUrl()).pathname).toBe(
      "/",
    );

    const blocked = await fetch(`${baseUrl()}/mw/blocked`);
    expect(blocked.status).toBe(403);
    expect(await blocked.text()).toContain("blocked by proxy.ts");
    expect(
      root.headers.getSetCookie().some((cookie) =>
        cookie.startsWith("ocel-proxy-seen=1"),
      ),
    ).toBe(true);
  });
};

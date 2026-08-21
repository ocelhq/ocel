import { expect, it } from "vitest";
import type { Check } from "../types";

export const checkEnv: Check = ({ baseUrl }) => {
  it("serves client and folder-scoped environment values", async () => {
    const page = await fetch(`${baseUrl()}/environment`);
    expect(page.status).toBe(200);
    const html = await page.text();
    expect(html).toContain(process.env.NEXT_PUBLIC_APP_ID!);
    expect(html).toContain(process.env.NEXT_PUBLIC_GA4_ID!);
    expect(html).not.toContain(process.env.SUPER_SECRET_VALUE!);

    const scoped = await fetch(`${baseUrl()}/api/environment`);
    expect(scoped.status).toBe(200);
    expect(await scoped.json()).toEqual({
      scoped: process.env.SOME_FOLDER_VALUE,
    });
  });
};

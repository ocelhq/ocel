import { expect, it } from "vitest";
import type { Check } from "../types";

export const checkLinks: Check = ({ baseUrl, linkReport }) => {
  it("answers through the consumed link", async () => {
    const response = await fetch(`${baseUrl()}/api/link`);
    expect(response.status).toBe(200);
    expect(await response.json()).toEqual(linkReport());
  });
};

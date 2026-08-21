import { expect, it } from "vitest";
import type { Check } from "../types";

export const checkStatic: Check = ({ baseUrl }) => {
  it("serves immutable public assets", async () => {
    const response = await fetch(`${baseUrl()}/file.svg`);
    expect(response.status).toBe(200);
    expect(response.headers.get("content-type")).toContain("image/svg+xml");
    expect(await response.text()).toContain("<svg");
  });
};

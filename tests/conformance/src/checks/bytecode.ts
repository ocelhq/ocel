import { expect, it } from "vitest";
import type { Check } from "../types";

export const checkBytecode: Check = ({ output, targetName }) => {
  if (targetName !== "aws") return;

  it("warms and embeds the Next server compile cache", () => {
    const deployment = output();
    const warmed = /warmed (\d+)\/(\d+) bundles/.exec(deployment);
    const embedded = /embedded (\d+)\/(\d+) compile caches/.exec(deployment);
    expect(warmed?.[2]).toMatch(/^[1-9]\d*$/);
    expect(warmed?.[1]).toBe(warmed?.[2]);
    expect(embedded?.[2]).toMatch(/^[1-9]\d*$/);
    expect(embedded?.[1]).toBe(embedded?.[2]);
  });
};

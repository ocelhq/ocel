import { expect, it } from "vitest";
import type { Check, CheckContext } from "../types";

export async function assertBytecodeConformance(
  context: Pick<
    CheckContext,
    | "output"
    | "assertBytecodeArchive"
    | "assertBytecodeEmbeddedArtifact"
    | "assertBytecodeColdStart"
  >,
) {
  const deployment = context.output();
  const warmed = /warmed (\d+)\/(\d+) bundles/.exec(deployment);
  const embedded = /embedded (\d+)\/(\d+) compile caches/.exec(deployment);
  expect(warmed?.[2]).toMatch(/^[1-9]\d*$/);
  expect(warmed?.[1]).toBe(warmed?.[2]);
  expect(embedded?.[2]).toMatch(/^[1-9]\d*$/);
  expect(embedded?.[1]).toBe(embedded?.[2]);
  await context.assertBytecodeArchive();
  await context.assertBytecodeEmbeddedArtifact();
  await context.assertBytecodeColdStart();
}

export const checkBytecode: Check = (context) => {
  if (context.targetName !== "aws") return;

  it("warms, embeds, and reads the Next server compile cache", async () => {
    await assertBytecodeConformance(context);
  });
};

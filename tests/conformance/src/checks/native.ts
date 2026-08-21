import { expect, it } from "vitest";
import { findDocument, uploadDocument } from "../http";
import type { Check } from "../types";

export const checkNative: Check = ({ example, baseUrl, runId }) => {
  it("runs the native thumbnail transform", async () => {
    const { key } = await uploadDocument(
      baseUrl(),
      `${example.name}-native-${runId}`,
    );
    const document = await findDocument(
      baseUrl(),
      key,
      (candidate) => candidate.thumbnail_key !== null,
    );
    expect(document?.thumbnail_key).toBe(`thumbnails/${key}.webp`);
  }, 60_000);
};

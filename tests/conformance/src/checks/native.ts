import { expect, it } from "vitest";
import { findDocument, uploadDocument } from "../http";
import type { Check } from "../types";

export const checkNative: Check = ({ example, baseUrl, headObject, runId }) => {
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
    const thumbnailKey = `thumbnails/${key}.webp`;
    expect(document?.thumbnail_key).toBe(thumbnailKey);
    const metadata = await headObject(thumbnailKey);
    expect(metadata.contentType).toBe("image/webp");
  }, 60_000);
};

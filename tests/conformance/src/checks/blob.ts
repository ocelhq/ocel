import { expect, it } from "vitest";
import { findDocument, uploadDocument } from "../http";
import type { Check } from "../types";

export const checkBlob: Check = ({ example, baseUrl, runId }) => {
  it("uploads a file and records it through onUploadComplete", async () => {
    const ownerId = `${example.name}-${runId}`;
    const { image, key } = await uploadDocument(baseUrl(), ownerId);
    expect(key).toContain(`documents/${ownerId}/photo.png`);
    const document = await findDocument(baseUrl(), key);
    expect(document).toBeDefined();
    expect(document).toMatchObject({
      key,
      name: "photo.png",
      mime_type: "image/png",
      size: String(image.byteLength),
      owner_id: ownerId,
    });
    expect(Object.keys(document!).sort()).toEqual([
      "id",
      "key",
      "mime_type",
      "name",
      "owner_id",
      "size",
      "thumbnail_key",
    ]);
  }, 60_000);
};

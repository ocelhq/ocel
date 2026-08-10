import { describe, expect, it } from "vitest";
import {
  canonicalUploadPayload,
  signUpload,
  verifyUpload,
  type SignedFile,
} from "./signing";

const file: SignedFile = {
  key: "org/proj/user/avatar.png",
  name: "avatar.png",
  size: 1024,
  mimeType: "image/png",
};

describe("upload callback signing", () => {
  it("round-trips: a signature made with the secret verifies", () => {
    const sig = signUpload("s3cret", "sess_1", file);
    expect(verifyUpload("s3cret", "sess_1", file, sig)).toBe(true);
  });

  it("rejects a signature made with a different secret", () => {
    const sig = signUpload("s3cret", "sess_1", file);
    expect(verifyUpload("other-secret", "sess_1", file, sig)).toBe(false);
  });

  it("rejects when the signed file identity is tampered", () => {
    const sig = signUpload("s3cret", "sess_1", file);
    expect(
      verifyUpload("s3cret", "sess_1", { ...file, size: 2048 }, sig),
    ).toBe(false);
  });

  it("rejects a garbage signature without throwing on length mismatch", () => {
    expect(verifyUpload("s3cret", "sess_1", file, "deadbeef")).toBe(false);
  });

  it("canonical payload is stable and field-ordered", () => {
    expect(canonicalUploadPayload("sess_1", file)).toBe(
      '{"sessionId":"sess_1","file":{"key":"org/proj/user/avatar.png","name":"avatar.png","size":1024,"mimeType":"image/png"}}',
    );
  });
});

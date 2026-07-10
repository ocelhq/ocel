import { describe, expect, it } from "vitest";
import { generateKey } from "./keys";

const file = { name: "photo.jpg", size: 10, mimeType: "image/jpeg" };

describe("generateKey", () => {
  it("uses prefix + sanitized name for the structured form", () => {
    expect(generateKey({ prefix: "avatars/" }, { file, metadata: {} })).toBe(
      "avatars/photo.jpg",
    );
  });

  it("defaults to just the sanitized name when no path is given", () => {
    expect(generateKey(undefined, { file, metadata: {} })).toBe("photo.jpg");
  });

  it("sanitizes unsafe characters and strips directory traversal", () => {
    expect(
      generateKey(
        { prefix: "x/" },
        { file: { ...file, name: "../a b/c!.png" }, metadata: {} },
      ),
    ).toBe("x/c-.png");
  });

  it("inserts a random token before the extension when randomSuffix is set", () => {
    const key = generateKey(
      { prefix: "avatars/", randomSuffix: true },
      { file, metadata: {} },
    );
    expect(key).toMatch(/^avatars\/photo-[a-z0-9]{8}\.jpg$/);
  });

  it("appends a token when the name has no extension", () => {
    const key = generateKey(
      { randomSuffix: true },
      { file: { ...file, name: "readme" }, metadata: {} },
    );
    expect(key).toMatch(/^readme-[a-z0-9]{8}$/);
  });

  it("hands full control to the function form with file + metadata ctx", () => {
    const key = generateKey(
      (ctx) => `u/${(ctx.metadata as { userId: string }).userId}/${ctx.file.name}`,
      { file, metadata: { userId: "42" } },
    );
    expect(key).toBe("u/42/photo.jpg");
  });
});

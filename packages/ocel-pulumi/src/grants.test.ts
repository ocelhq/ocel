import { describe, expect, it } from "vitest";
import { scoped } from "./grants.js";

describe("the grants a binding publishes", () => {
  it("keeps a role on a named resource", () => {
    const grants = [{ role: "roles/storage.objectViewer", resource: "projects/_/buckets/images" }];

    expect(scoped("images", grants)).toEqual(grants);
  });

  it("refuses every role", () => {
    expect(() => scoped("images", [{ role: "*", resource: "projects/_/buckets/images" }])).toThrow(
      /"\*" is every role/,
    );
  });

  it("refuses a role on no resource", () => {
    expect(() => scoped("images", [{ role: "roles/storage.objectViewer", resource: "" }])).toThrow(
      /resource it binds and nothing else/,
    );
  });

  it("refuses a grant that is both a role and actions", () => {
    expect(() =>
      scoped("images", [
        {
          role: "roles/storage.objectViewer",
          resource: "projects/_/buckets/images",
          actions: ["s3:GetObject"],
          resources: ["arn:aws:s3:::images/*"],
        } as never,
      ]),
    ).toThrow(/never both/);
  });
});

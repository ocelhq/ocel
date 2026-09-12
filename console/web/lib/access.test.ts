import { auth } from "@console/auth";
import { db } from "@console/db";
import { user } from "@console/db/schema";
import { eq } from "drizzle-orm";
import { beforeAll, describe, expect, it } from "vitest";
import { createTestSessionWithOrganization } from "@/test/auth-harness";
import { setupTestDatabase } from "@/test/db";
import { resolveAccess } from "./access";
import { safeRedirect } from "./request-path";

describe("resolveAccess", () => {
  beforeAll(async () => {
    await setupTestDatabase();
  });

  it("is signed-out without a session", async () => {
    expect(await resolveAccess(new Headers())).toEqual({ state: "signed-out" });
  });

  it("is no-organization for a user who belongs to none", async () => {
    const email = `no-org-${crypto.randomUUID()}@example.test`;
    const signedUp = await auth.api.signUpEmail({
      body: { name: "No Org", email, password: "password1234" },
    });

    try {
      const access = await resolveAccess(
        new Headers({ Authorization: `Bearer ${signedUp.token}` }),
      );
      expect(access).toEqual({ state: "no-organization", userId: signedUp.user.id });
    } finally {
      await db.delete(user).where(eq(user.id, signedUp.user.id));
    }
  });

  it("activates an existing organization on a fresh sign-in", async () => {
    const session = await createTestSessionWithOrganization();

    try {
      const signedIn = await auth.api.signInEmail({
        body: { email: session.user.email, password: "password1234" },
      });
      const access = await resolveAccess(
        new Headers({ Authorization: `Bearer ${signedIn.token}` }),
      );
      expect(access).toEqual({
        state: "ready",
        session: { userId: session.user.id, activeOrganizationId: session.organization.id },
      });
    } finally {
      await session.cleanup();
    }
  });
});

describe("safeRedirect", () => {
  it.each([
    ["/projects?tab=all", "/projects?tab=all"],
    [undefined, "/"],
    ["https://evil.test", "/"],
    ["//evil.test", "/"],
    ["/\\evil.test", "/"],
  ])("maps %s to %s", (target, expected) => {
    expect(safeRedirect(target)).toBe(expected);
  });
});

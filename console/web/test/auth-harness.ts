import { auth } from "@console/auth";
import { db } from "@console/db";
import { organization, user } from "@console/db/schema";
import { eq } from "drizzle-orm";

export async function createTestSessionWithOrganization() {
  const suffix = crypto.randomUUID();
  const email = `test-${suffix}@example.test`;

  const signUpResult = await auth.api.signUpEmail({
    body: {
      name: "Test User",
      email,
      password: "password1234",
    },
  });

  if (!signUpResult.token) {
    throw new Error("signUpEmail did not return a session token");
  }

  const headers = new Headers({
    Authorization: `Bearer ${signUpResult.token}`,
  });

  const createdOrganization = await auth.api.createOrganization({
    body: {
      name: "Test Org",
      slug: `test-org-${suffix}`,
    },
    headers,
  });

  if (!createdOrganization) {
    throw new Error("createOrganization did not return an organization");
  }

  return {
    user: signUpResult.user,
    token: signUpResult.token,
    headers,
    organization: createdOrganization,
    async cleanup() {
      await db
        .delete(organization)
        .where(eq(organization.id, createdOrganization.id));
      await db.delete(user).where(eq(user.id, signUpResult.user.id));
    },
  };
}

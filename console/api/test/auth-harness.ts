import { auth } from "@console/auth/next";
import { db } from "@console/db";
import { organization, user } from "@console/db/schema";
import { eq } from "drizzle-orm";

async function signUpTestUser(suffix: string) {
  const signUpResult = await auth.api.signUpEmail({
    body: {
      name: "Test User",
      email: `test-${suffix}@example.test`,
      password: "password1234",
    },
  });

  if (!signUpResult.token) {
    throw new Error("signUpEmail did not return a session token");
  }

  return {
    user: signUpResult.user,
    token: signUpResult.token,
    headers: new Headers({ Authorization: `Bearer ${signUpResult.token}` }),
  };
}

export async function createTestSessionWithRole(organizationId: string, role: string) {
  const joined = await signUpTestUser(crypto.randomUUID());

  await auth.api.addMember({
    body: { userId: joined.user.id, organizationId, role },
  });
  await auth.api.setActiveOrganization({
    body: { organizationId },
    headers: joined.headers,
  });

  return {
    ...joined,
    async cleanup() {
      await db.delete(user).where(eq(user.id, joined.user.id));
    },
  };
}

export async function createTestSessionWithOrganization() {
  const suffix = crypto.randomUUID();
  const signUpResult = await signUpTestUser(suffix);
  const { headers } = signUpResult;

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
      await db.delete(organization).where(eq(organization.id, createdOrganization.id));
      await db.delete(user).where(eq(user.id, signUpResult.user.id));
    },
  };
}

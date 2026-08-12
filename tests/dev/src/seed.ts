import { applyE2EEnvDefaults } from "./env";

applyE2EEnvDefaults();

const { auth } = await import("@console/auth/next");

export type Seed = {
  token: string;
  userId: string;
  organizationId: string;
};

export async function seed(): Promise<Seed> {
  const suffix = crypto.randomUUID();
  const email = `e2e-${suffix}@example.test`;

  const signUp = await auth.api.signUpEmail({
    body: { name: "E2E User", email, password: "password1234" },
  });
  if (!signUp.token) {
    throw new Error("signUpEmail did not return a session token");
  }

  const headers = new Headers({ Authorization: `Bearer ${signUp.token}` });
  const org = await auth.api.createOrganization({
    body: { name: "E2E Org", slug: `e2e-org-${suffix}` },
    headers,
  });
  if (!org) {
    throw new Error("createOrganization did not return an organization");
  }

  return {
    token: signUp.token,
    userId: signUp.user.id,
    organizationId: org.id,
  };
}

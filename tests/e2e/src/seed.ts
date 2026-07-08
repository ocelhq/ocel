import { applyE2EEnvDefaults } from "./env";

applyE2EEnvDefaults();

// Import AFTER env defaults are applied: @repo/db resolves its connection at
// import time from OCEL_RESOURCE_POSTGRES_main.
const { auth } = await import("@repo/auth/next");

export type Seed = {
  token: string;
  userId: string;
  organizationId: string;
};

// Mints one real Better-Auth session (via signUpEmail) plus one organization
// and membership - the identity every example's `ocel init` runs against.
// Adapted from packages/api/test/auth-harness.ts, minus cleanup: e2e rows are
// intentionally left behind (the databases they seed are per-run anyway).
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

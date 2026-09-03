import { applyConsoleEnvDefaults } from "./env";

applyConsoleEnvDefaults();

const { auth } = await import("@console/auth/next");

export type Seed = {
  token: string;
  userId: string;
  organizationId: string;
};

export async function seed(label: string): Promise<Seed> {
  const suffix = crypto.randomUUID();
  const slug = label.toLowerCase();

  const signUp = await auth.api.signUpEmail({
    body: {
      name: `${label} User`,
      email: `${slug}-${suffix}@example.test`,
      password: "password1234",
    },
  });
  if (!signUp.token) {
    throw new Error("signUpEmail did not return a session token");
  }

  const org = await auth.api.createOrganization({
    body: { name: `${label} Org`, slug: `${slug}-org-${suffix}` },
    headers: new Headers({ Authorization: `Bearer ${signUp.token}` }),
  });
  if (!org) {
    throw new Error("createOrganization did not return an organization");
  }

  return { token: signUp.token, userId: signUp.user.id, organizationId: org.id };
}

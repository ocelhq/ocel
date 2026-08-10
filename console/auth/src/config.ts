import { db } from "@console/db";
import * as schema from "@console/db/schema";
import type { BetterAuthOptions } from "better-auth";
import { drizzleAdapter } from "better-auth/adapters/drizzle";
import { bearer, deviceAuthorization, organization } from "better-auth/plugins";
import { OCEL_CLI_CLIENT_ID } from "./constants";

export const authConfig = {
  database: drizzleAdapter(db, {
    provider: "pg",
    schema,
  }),
  emailAndPassword: {
    enabled: true,
  },
  socialProviders: {
    github: {
      clientId: process.env.GITHUB_CLIENT_ID as string,
      clientSecret: process.env.GITHUB_CLIENT_SECRET as string,
    },
  },
  session: {
    expiresIn: 60 * 60 * 24 * 30,
  },
  plugins: [
    organization(),
    bearer(),
    deviceAuthorization({
      verificationUri: "/device",
      validateClient: async (clientId) => clientId === OCEL_CLI_CLIENT_ID,
    }),
  ],
} satisfies BetterAuthOptions;

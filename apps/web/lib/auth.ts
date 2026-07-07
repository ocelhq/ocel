import { db } from "@repo/db";
import * as schema from "@repo/db/schema";
import { betterAuth } from "better-auth";
import { drizzleAdapter } from "better-auth/adapters/drizzle";
import { nextCookies } from "better-auth/next-js";
import { bearer, deviceAuthorization, organization } from "better-auth/plugins";
import { OCEL_CLI_CLIENT_ID } from "./constants";

export const auth = betterAuth({
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
    // 30 days. The Ocel CLI's device-authorization login relies on this
    // session token directly (there is no separate refresh_token in the
    // device flow), so it's kept long-lived with rolling renewal on use.
    expiresIn: 60 * 60 * 24 * 30,
  },
  plugins: [
    organization(),
    // Lets the CLI (and any other Bearer-token client) authenticate API
    // requests with `Authorization: Bearer <access_token>` instead of
    // cookies.
    bearer(),
    // Powers `ocel login`: RFC 8628 device authorization grant.
    deviceAuthorization({
      verificationUri: "/device",
      validateClient: async (clientId) => clientId === OCEL_CLI_CLIENT_ID,
    }),
    nextCookies(),
  ],
});

export type Session = typeof auth.$Infer.Session;

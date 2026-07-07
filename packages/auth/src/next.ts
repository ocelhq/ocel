import { betterAuth } from "better-auth";
import { nextCookies } from "better-auth/next-js";
import { authConfig } from "./config";

// Next.js-specific instance: identical config to the root `auth`, plus
// `nextCookies()` so Server Actions/Components calling `auth.api.*` directly
// get their session cookies set automatically. Must stay the last plugin -
// better-auth applies it as a response-cookie hook over whatever plugins
// precede it.
export const auth = betterAuth({
  ...authConfig,
  plugins: [...authConfig.plugins, nextCookies()],
});

export type Session = typeof auth.$Infer.Session;

import { betterAuth } from "better-auth";
import { nextCookies, toNextJsHandler } from "better-auth/next-js";
import { authConfig } from "./config";

export const auth = betterAuth({
  ...authConfig,
  plugins: [...authConfig.plugins, nextCookies()],
});

export type Session = typeof auth.$Infer.Session;

export { toNextJsHandler };

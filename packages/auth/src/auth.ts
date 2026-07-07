import { betterAuth } from "better-auth";
import { authConfig } from "./config";

export const auth = betterAuth(authConfig);

export type Session = typeof auth.$Infer.Session;

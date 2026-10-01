import { OCEL_DEV_SERVER } from "../runtime/dev-server.js";

declare global {
  var __ocelRegister: Promise<unknown>[];
}

/** Holds a registration with the dev server until the app's declarations are collected. Throws when no dev server is running. */
export function defer(p: Promise<any>) {
  if (!OCEL_DEV_SERVER) {
    throw new Error("OCEL_DEV_SERVER environment variable is not set");
  }

  globalThis.__ocelRegister ??= [];
  globalThis.__ocelRegister.push(p);
}

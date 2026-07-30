import { OCEL_DEV_SERVER } from "./constants.js";

declare global {
  var __ocelRegister: Promise<unknown>[];
}

export function defer(p: Promise<any>) {
  if (!OCEL_DEV_SERVER) {
    throw new Error("OCEL_DEV_SERVER environment variable is not set");
  }

  globalThis.__ocelRegister ??= [];
  globalThis.__ocelRegister.push(p);
}

import { readPortBind } from "@framework/node-runtime/host";
import { installNextHost, type NextHost } from "./host.mjs";

export async function serveNextContainer(host: NextHost): Promise<void> {
  installNextHost({ ...host, bind: readPortBind(process.env) });
  await import("./entrypoint.mjs");
}

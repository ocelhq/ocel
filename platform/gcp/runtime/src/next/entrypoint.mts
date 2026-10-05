import { installNextHost } from "@framework/next-runtime/host";
import { newGcpNextHost } from "./next-host.mjs";

installNextHost(newGcpNextHost(process.env));

await import("@framework/next-runtime/entrypoint");

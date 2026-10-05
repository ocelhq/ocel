import { installNextHost } from "@framework/next-runtime/host";
import { newAwsNextHost } from "./next-host.mjs";

installNextHost(newAwsNextHost(process.env));

await import("@framework/next-runtime/entrypoint");

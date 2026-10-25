import { serveNextContainer } from "@framework/next-runtime/container-entry";
import { newGcpNextHost } from "./next-host.mjs";

await serveNextContainer(newGcpNextHost(process.env));

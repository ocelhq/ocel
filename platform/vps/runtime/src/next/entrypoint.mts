import { serveNextContainer } from "@framework/next-runtime/container-entry";
import { newVpsNextHost } from "./next-host.mjs";

await serveNextContainer(newVpsNextHost(process.env));

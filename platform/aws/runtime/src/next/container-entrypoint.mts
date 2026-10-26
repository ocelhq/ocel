import { serveNextContainer } from "@framework/next-runtime/container-entry";
import { newAwsFunctionContainerHost } from "./function-container-host.mjs";

await serveNextContainer(newAwsFunctionContainerHost(process.env));

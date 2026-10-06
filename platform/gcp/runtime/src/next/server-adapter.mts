import { dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { installNextHost } from "@framework/next-runtime/host";
import { newServerAdapter } from "@framework/next-runtime/server-adapter";
import { newGcpNextHost } from "./next-host.mjs";

export default newServerAdapter(dirname(fileURLToPath(import.meta.url)), () =>
  installNextHost(newGcpNextHost(process.env)),
);

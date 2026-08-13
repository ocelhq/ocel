import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { loadEnvFile } from "./env.mjs";

const path = resolve(fileURLToPath(import.meta.url), "..", "..", ".env.local");
const applied = loadEnvFile(path);

if (applied.length > 0) {
  process.stderr.write(`[bench] read ${applied.join(", ")} from ${path}\n`);
}

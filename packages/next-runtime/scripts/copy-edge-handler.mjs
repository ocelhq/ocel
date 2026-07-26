// The cache handler ships as source, not as compiled output: modifyConfig copies
// it verbatim into the app being built, and tsc neither reads nor emits a .cjs.
// It has to sit beside the built adapter, which resolves it relative to itself.
import { copyFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const pkgDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const name = "edge-cache-handler.cjs";

await copyFile(join(pkgDir, "src", name), join(pkgDir, "dist", name));

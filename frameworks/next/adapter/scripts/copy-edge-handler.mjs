import { copyFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import { runtimeFiles } from "./runtime-files.mjs";

const pkgDir = join(dirname(fileURLToPath(import.meta.url)), "..");

await Promise.all(
  runtimeFiles.map((name) =>
    copyFile(join(pkgDir, "src", name), join(pkgDir, "dist", name)),
  ),
);

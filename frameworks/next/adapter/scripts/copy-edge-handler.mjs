import { copyFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const pkgDir = join(dirname(fileURLToPath(import.meta.url)), "..");
const names = ["edge-cache-handler.cjs", "next-dispatch.cjs"];

await Promise.all(
  names.map((name) =>
    copyFile(join(pkgDir, "src", name), join(pkgDir, "dist", name)),
  ),
);

import { randomBytes } from "node:crypto";
import { appendFile, chmod, readFile } from "node:fs/promises";
import { spawn } from "node:child_process";

const root = new URL("../", import.meta.url);
const file = new URL(".env", root);
let current = "";
try {
  current = await readFile(file, "utf8");
} catch (error) {
  if (error.code !== "ENOENT") throw error;
}

const additions = [];
const missing = (name) =>
  !process.env[name] && !new RegExp(`^${name}=`, "m").test(current);

if (missing("OCEL_BLOB_ACCESS_KEY_ID")) {
  additions.push(
    `OCEL_BLOB_ACCESS_KEY_ID=ocel-local-${randomBytes(8).toString("hex")}`,
  );
}
if (missing("OCEL_BLOB_SECRET_ACCESS_KEY")) {
  additions.push(
    `OCEL_BLOB_SECRET_ACCESS_KEY=${randomBytes(32).toString("hex")}`,
  );
}

if (additions.length > 0) {
  const separator = current.length > 0 && !current.endsWith("\n") ? "\n" : "";
  await appendFile(file, `${separator}${additions.join("\n")}\n`, {
    mode: 0o600,
  });
  await chmod(file, 0o600);
}

const code = await new Promise((resolve, reject) => {
  const child = spawn("docker", ["compose", "up", "-d"], {
    cwd: root,
    stdio: "inherit",
  });
  child.on("error", reject);
  child.on("exit", resolve);
});

process.exitCode = code ?? 1;

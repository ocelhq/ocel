import { db } from "../../../shared/index.js";

await import("./jobs.js");

const spec = "../../../shared/" + ["met", "rics"].join("") + ".js";
try {
  await import(spec);
} catch (err) {
  process.stdout.write("\nruntime-computed import failed: " + String(err) + "\n");
}

export function run() {
  return db;
}

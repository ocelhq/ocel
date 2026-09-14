import { readFileSync } from "node:fs";
import { join } from "node:path";

const LIVE_DIR = "OCEL_LIVE_DIR";

export function readLiveFile(key: string): string | undefined {
  const dir = process.env[LIVE_DIR];
  if (!dir) return undefined;
  try {
    return readFileSync(join(dir, key), "utf8");
  } catch {
    return undefined;
  }
}

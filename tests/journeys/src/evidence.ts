import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { redact } from "./contract";
import type { Leg } from "./spec";

export type Evidence = {
  dir: string;
  write(leg: Leg, name: string, content: string): Promise<void>;
};

export function evidence(dir: string): Evidence {
  return {
    dir,
    async write(leg, name, content) {
      const target = path.join(dir, leg);
      await mkdir(target, { recursive: true });
      await writeFile(path.join(target, name), redact(content), "utf8");
    },
  };
}

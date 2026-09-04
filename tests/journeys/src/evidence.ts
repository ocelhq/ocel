import { appendFile, mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { redact } from "./contract";
import type { Leg } from "./spec";

export type Evidence = {
  dir: string;
  write(leg: Leg, name: string, content: string): Promise<void>;
  append(name: string, line: string): Promise<void>;
};

export function evidence(dir: string): Evidence {
  return {
    dir,
    async write(leg, name, content) {
      const target = path.join(dir, leg);
      await mkdir(target, { recursive: true });
      await writeFile(path.join(target, name), redact(content), "utf8");
    },
    async append(name, line) {
      await mkdir(dir, { recursive: true });
      await appendFile(path.join(dir, name), `${redact(line)}\n`, "utf8");
    },
  };
}

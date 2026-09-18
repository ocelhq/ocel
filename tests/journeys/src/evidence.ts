import { appendFile, mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { redact } from "./checks/context";
import type { Phase } from "./matrix/types";

export type Evidence = {
  dir: string;
  write(phase: Phase, name: string, content: string): Promise<void>;
  append(name: string, line: string): Promise<void>;
};

export function evidence(dir: string): Evidence {
  return {
    dir,
    async write(phase, name, content) {
      const target = path.join(dir, phase);
      await mkdir(target, { recursive: true });
      await writeFile(path.join(target, name), redact(content), "utf8");
    },
    async append(name, line) {
      await mkdir(dir, { recursive: true });
      await appendFile(path.join(dir, name), `${redact(line)}\n`, "utf8");
    },
  };
}

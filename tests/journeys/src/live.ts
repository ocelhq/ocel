import { appendFileSync, closeSync, fstatSync, openSync, readSync } from "node:fs";
import type { Readable } from "node:stream";
import { redact } from "./contract";

export const LIVE_ENV = "OCEL_JOURNEY_LIVE";

export type Say = (line: string) => void;

export function live(prefix: string, env: NodeJS.ProcessEnv = process.env): Say {
  const file = env[LIVE_ENV];
  const sink = file
    ? (text: string) => appendFileSync(file, text, "utf8")
    : (text: string) => process.stderr.write(text);
  return (line) => sink(`${prefix} ${redact(line)}\n`);
}

export function lines(onLine: Say): { push(chunk: string | Buffer): void; end(): void } {
  let carry = "";
  return {
    push(chunk) {
      carry += String(chunk);
      const parts = carry.split(/\r?\n/);
      carry = parts.pop() ?? "";
      for (const part of parts) {
        onLine(part);
      }
    },
    end() {
      if (carry !== "") {
        onLine(carry);
        carry = "";
      }
    },
  };
}

export function relay(stream: Readable | null | undefined, say: Say): void {
  if (!stream) {
    return;
  }
  const split = lines(say);
  stream.on("data", (chunk) => split.push(chunk));
  stream.on("end", () => split.end());
}

export function follow(
  file: string,
  out: (text: Buffer) => void = (text) => process.stderr.write(text),
  everyMs = 250,
): () => void {
  let offset = 0;
  const drain = () => {
    let fd: number;
    try {
      fd = openSync(file, "r");
    } catch {
      return;
    }
    try {
      const size = fstatSync(fd).size;
      if (size > offset) {
        const buf = Buffer.alloc(size - offset);
        const read = readSync(fd, buf, 0, buf.length, offset);
        offset += read;
        out(buf.subarray(0, read));
      }
    } finally {
      closeSync(fd);
    }
  };
  const timer = setInterval(drain, everyMs);
  return () => {
    clearInterval(timer);
    drain();
  };
}

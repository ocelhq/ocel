import { existsSync, readFileSync } from "node:fs";

export interface WireVector {
  name: string;
  namespace: string;
  pattern: string;
  wildcard: boolean;
  params: Record<string, string>;
  channel?: string;
  error?: string;
}

export interface RealtimeVectors {
  wire: WireVector[];
}

function findRepoRoot() {
  let dir = new URL("./", import.meta.url);
  while (!existsSync(new URL("go.work", dir))) {
    const parent = new URL("../", dir);
    if (parent.href === dir.href) throw new Error("no go.work found above the ocel package");
    dir = parent;
  }
  return dir;
}

export function readRealtimeVectors(): RealtimeVectors {
  return JSON.parse(readFileSync(new URL("proto/realtime/vectors.json", findRepoRoot()), "utf8"));
}

export function readRealtimeBindingFixture(): string {
  return readFileSync(
    new URL("proto/common/bindings/v1/fixtures/realtime.json", findRepoRoot()),
    "utf8",
  );
}

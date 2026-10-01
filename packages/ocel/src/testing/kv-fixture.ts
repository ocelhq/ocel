import { existsSync, readFileSync } from "node:fs";

export interface KvFixture {
  keys: { pattern: string; params: Record<string, string>; key: string }[];
  values: {
    text: { value: string; stored: string }[];
    counter: { value: number; stored: string }[];
    json: { value: unknown; stored: string }[];
    list: { value: string[]; stored: string[] }[];
    set: { value: string[]; stored: string[] }[];
  };
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

export function readKvFixture(): KvFixture {
  return JSON.parse(
    readFileSync(new URL("proto/app/resources/v1/fixtures/kv.json", findRepoRoot()), "utf8"),
  );
}

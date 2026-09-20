const LIVE_DIR = "OCEL_LIVE_DIR";

interface FileSystem {
  readFileSync(path: string, encoding: "utf8"): string;
}

interface Host {
  env?: Record<string, string | undefined>;
  versions?: { node?: string };
  getBuiltinModule?: (id: string) => unknown;
}

function host(): Host | undefined {
  return (globalThis as { process?: Host }).process;
}

function fileSystem(on: Host): FileSystem | undefined {
  const loaded = on.getBuiltinModule?.("node:fs");
  if (loaded) return loaded as FileSystem;
  if (!on.versions?.node) return undefined;
  throw new Error(
    `${LIVE_DIR} is set, and reading the bindings it projects needs node 22.3 or newer. This app runs on node ${on.versions.node}.`,
  );
}

export function readLiveFile(key: string): string | undefined {
  const on = host();
  const dir = on?.env?.[LIVE_DIR];
  if (!on || !dir) return undefined;

  const fs = fileSystem(on);
  if (!fs) return undefined;

  try {
    return fs.readFileSync(`${dir}/${key}`, "utf8");
  } catch {
    return undefined;
  }
}

import { lstatSync, readdirSync } from "node:fs";
import { join } from "node:path";

// AWS Lambda's ceiling on an unzipped direct-upload artifact.
export const defaultBudgetBytes = 200 * 1024 * 1024;

export interface Bundle<T> {
  name: string;
  members: T[];
  // The union of every member's assets: dest-key relative to the repo root,
  // absolute source path on disk.
  assets: Record<string, string>;
  sizeBytes: number;
}

export interface PackOptions<T> {
  entryKeyOf: (member: T) => string;
  assetsOf: (member: T) => Record<string, string>;
  // Members that cannot share one Lambda. Defaults to a single partition:
  // Next's maxDuration/preferredRegion are per-function settings, so once they
  // are plumbed through they become this key and the packer stays unchanged.
  partitionBy?: (member: T) => string;
  budgetBytes?: number;
  sizeOf?: (absPath: string) => number;
}

// packBundles packs members into the fewest Lambda artifacts their assets fit
// in. Routes in a Next build share nearly all of their traced assets — the
// node_modules forest and the chunk set — so a bundle's cost is the union of its
// members' assets and a second route usually costs only its delta. Members are
// packed in entry-key order and bundles named in the order they are opened, so
// identical input always yields an identical artifact.
export function packBundles<T>(
  members: readonly T[],
  opts: PackOptions<T>,
): Bundle<T>[] {
  const {
    entryKeyOf,
    assetsOf,
    partitionBy = () => "",
    budgetBytes = defaultBudgetBytes,
    sizeOf = sizeOfPath,
  } = opts;

  assertOneSourcePerKey(members, assetsOf);

  const sizes = new Map<string, number>();
  const sizeOfCached = (abs: string) => {
    let size = sizes.get(abs);
    if (size === undefined) {
      size = sizeOf(abs);
      sizes.set(abs, size);
    }
    return size;
  };

  const partitions = new Map<string, T[]>();
  for (const member of [...members].sort(byKey(entryKeyOf))) {
    const key = partitionBy(member);
    const group = partitions.get(key);
    if (group) group.push(member);
    else partitions.set(key, [member]);
  }

  const bundles: Bundle<T>[] = [];
  const open = (): Bundle<T> => {
    const bundle = {
      name: `bundle-${bundles.length}`,
      members: [],
      assets: {},
      sizeBytes: 0,
    };
    bundles.push(bundle);
    return bundle;
  };

  for (const key of [...partitions.keys()].sort()) {
    let bundle = open();

    for (const member of partitions.get(key)!) {
      const added = Object.entries(assetsOf(member)).filter(
        ([dest]) => !(dest in bundle.assets),
      );
      const delta = added.reduce((sum, [, abs]) => sum + sizeOfCached(abs), 0);

      if (bundle.members.length > 0 && bundle.sizeBytes + delta > budgetBytes) {
        bundle = open();
        for (const [dest, abs] of Object.entries(assetsOf(member))) {
          bundle.assets[dest] = abs;
          bundle.sizeBytes += sizeOfCached(abs);
        }
      } else {
        for (const [dest, abs] of added) bundle.assets[dest] = abs;
        bundle.sizeBytes += delta;
      }
      bundle.members.push(member);

      // Nothing can be packed with a member that overflows the budget alone, so
      // it ships as-is and AWS rejects the artifact with its own clear error.
      if (bundle.members.length === 1 && bundle.sizeBytes > budgetBytes) {
        console.warn(
          `ocel: "${entryKeyOf(member)}" traces ${bundle.sizeBytes} bytes of assets, over the ${budgetBytes}-byte function limit — it cannot be split any further`,
        );
        bundle = open();
      }
    }

    if (bundle.members.length === 0) bundles.pop();
  }

  return bundles;
}

function byKey<T>(entryKeyOf: (member: T) => string) {
  return (a: T, b: T) => (entryKeyOf(a) < entryKeyOf(b) ? -1 : 1);
}

// Dest-keys are repo-root-relative, so two members naming one key with
// different sources should be impossible — and a silent last-writer-wins copy
// would be far harder to find than this.
function assertOneSourcePerKey<T>(
  members: readonly T[],
  assetsOf: (member: T) => Record<string, string>,
): void {
  const sourceByDest = new Map<string, string>();
  for (const member of members) {
    for (const [dest, abs] of Object.entries(assetsOf(member))) {
      const seen = sourceByDest.get(dest);
      if (seen !== undefined && seen !== abs) {
        throw new Error(
          `ocel: asset "${dest}" maps to two different sources — "${seen}" and "${abs}"`,
        );
      }
      sourceByDest.set(dest, abs);
    }
  }
}

// A path's size on disk as the asset copy will land it: a directory asset is
// copied whole, so it costs its recursive contents; a symlink is preserved as a
// link and costs only itself, with its target copied under its own asset entry.
// A path that has vanished costs nothing, matching the copy skipping it.
function sizeOfPath(absPath: string): number {
  let info;
  try {
    info = lstatSync(absPath);
  } catch {
    return 0;
  }
  if (!info.isDirectory()) return info.size;

  let total = 0;
  for (const entry of readdirSync(absPath, { withFileTypes: true })) {
    total += sizeOfPath(join(absPath, entry.name));
  }
  return total;
}

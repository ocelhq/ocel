import { lstatSync, readdirSync } from "node:fs";
import { join } from "node:path";

export const defaultBudgetBytes = 200 * 1024 * 1024;

export interface PackedMember<T> {
  member: T;
  sizeBytes: number;
}

export interface Bundle<T> {
  name: string;
  members: PackedMember<T>[];
  assets: Record<string, string>;
  sizeBytes: number;
}

export interface PackResult<T> {
  bundles: Bundle<T>[];
  missingAssets: string[];
}

export interface PackOptions<T> {
  entryKeyOf: (member: T) => string;
  assetsOf: (member: T) => Record<string, string>;
  partitionBy?: (member: T) => string;
  budgetBytes?: number;
  sizeOf?: (absPath: string) => number | undefined;
  seedAssets?: Record<string, string>;
}

export function packBundles<T>(
  members: readonly T[],
  opts: PackOptions<T>,
): PackResult<T> {
  const {
    entryKeyOf,
    assetsOf,
    partitionBy = () => "",
    budgetBytes = defaultBudgetBytes,
    sizeOf = sizeOfPath,
  } = opts;

  assertUniqueEntryKeys(members, entryKeyOf);

  const sizes = new Map<string, number | undefined>();
  const sizeOfCached = (abs: string) => {
    if (!sizes.has(abs)) sizes.set(abs, sizeOf(abs));
    return sizes.get(abs);
  };
  const missing = new Set<string>();

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

  const bytesOf = (entries: [string, string][]) =>
    entries.reduce((sum, [, abs]) => sum + (sizeOfCached(abs) ?? 0), 0);

  const absorb = (bundle: Bundle<T>, assets: Record<string, string>) => {
    for (const [dest, abs] of Object.entries(assets)) {
      if (dest in bundle.assets) continue;
      bundle.assets[dest] = abs;
      const size = sizeOfCached(abs);
      if (size === undefined) missing.add(dest);
      else bundle.sizeBytes += size;
    }
  };

  for (const key of [...partitions.keys()].sort()) {
    let bundle = open();

    for (const member of partitions.get(key)!) {
      const assets = assetsOf(member);
      const entries = Object.entries(assets);
      const conflicts = entries.some(
        ([dest, abs]) => dest in bundle.assets && bundle.assets[dest] !== abs,
      );
      const delta = bytesOf(
        entries.filter(([dest]) => !(dest in bundle.assets)),
      );

      if (
        bundle.members.length > 0 &&
        (conflicts || bundle.sizeBytes + delta > budgetBytes)
      ) {
        bundle = open();
      }
      absorb(bundle, assets);
      bundle.members.push({ member, sizeBytes: bytesOf(entries) });

      if (bundle.members.length === 1 && bundle.sizeBytes > budgetBytes) {
        console.warn(
          `ocel: "${entryKeyOf(member)}" traces ${bundle.sizeBytes} bytes of assets, over the ${budgetBytes}-byte function limit — it cannot be split any further`,
        );
        bundle = open();
      }
    }

    if (bundle.members.length === 0) bundles.pop();
  }

  if (opts.seedAssets) {
    const bundle = bundles[0] ?? open();
    absorb(bundle, opts.seedAssets);
    if (bundle.sizeBytes > budgetBytes) {
      console.warn(
        `ocel: node middleware pushes "${bundle.name}" to ${bundle.sizeBytes} bytes, over the ${budgetBytes}-byte function limit — it must run in the bundle the manifest already calls it through and cannot be split any further`,
      );
    }
  }

  return { bundles, missingAssets: [...missing].sort() };
}

function byKey<T>(entryKeyOf: (member: T) => string) {
  return (a: T, b: T) => {
    const left = entryKeyOf(a);
    const right = entryKeyOf(b);
    return left < right ? -1 : left > right ? 1 : 0;
  };
}

function assertUniqueEntryKeys<T>(
  members: readonly T[],
  entryKeyOf: (member: T) => string,
): void {
  const seen = new Set<string>();
  for (const member of members) {
    const key = entryKeyOf(member);
    if (seen.has(key)) {
      throw new Error(`ocel: two members share the entry key "${key}"`);
    }
    seen.add(key);
  }
}

function sizeOfPath(absPath: string): number | undefined {
  let info;
  try {
    info = lstatSync(absPath);
  } catch {
    return undefined;
  }
  if (!info.isDirectory()) return info.size;

  let total = 0;
  for (const entry of readdirSync(absPath, { withFileTypes: true })) {
    total += sizeOfPath(join(absPath, entry.name)) ?? 0;
  }
  return total;
}

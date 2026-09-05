import { HARNESS_PREFIX } from "../../identity";

export type Stranded = { slug: string; cell: string };

export type Sweepable = { reclaim: Stranded[]; unreadable: string[] };

export function reclaimable(slug: string, cells: string[]): Stranded | undefined {
  if (!slug.startsWith(HARNESS_PREFIX)) {
    return undefined;
  }
  for (const cell of [...cells].sort((a, b) => b.length - a.length)) {
    const suffix = `-${cell}`;
    if (!slug.endsWith(suffix)) {
      continue;
    }
    if (slug.length - suffix.length > HARNESS_PREFIX.length) {
      return { slug, cell };
    }
  }
  return undefined;
}

export function sweepable(found: string[], keep: Iterable<string>, cells: string[]): Sweepable {
  const mine = new Set(keep);
  const seen = new Set<string>();
  const reclaim: Stranded[] = [];
  const unreadable: string[] = [];
  for (const slug of found) {
    if (mine.has(slug) || seen.has(slug) || !slug.startsWith(HARNESS_PREFIX)) {
      continue;
    }
    seen.add(slug);
    const stranded = reclaimable(slug, cells);
    if (stranded) {
      reclaim.push(stranded);
    } else {
      unreadable.push(slug);
    }
  }
  return { reclaim, unreadable };
}

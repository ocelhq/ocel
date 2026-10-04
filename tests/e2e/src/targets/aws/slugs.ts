import { HARNESS_PREFIX } from "../../identity";

export type Stranded = { slug: string; cell: string | undefined };

export function reclaimable(slug: string, cells: string[]): Stranded | undefined {
  if (!slug.startsWith(HARNESS_PREFIX)) {
    return undefined;
  }
  for (const cell of [...cells].sort((a, b) => b.length - a.length)) {
    if (slug.endsWith(`-${cell}`)) {
      return { slug, cell };
    }
  }
  return { slug, cell: undefined };
}

export function sweepable(found: string[], keep: Iterable<string>, cells: string[]): Stranded[] {
  const mine = new Set(keep);
  const seen = new Set<string>();
  const reclaim: Stranded[] = [];
  for (const slug of found) {
    if (mine.has(slug) || seen.has(slug)) {
      continue;
    }
    seen.add(slug);
    const stranded = reclaimable(slug, cells);
    if (stranded) {
      reclaim.push(stranded);
    }
  }
  return reclaim;
}

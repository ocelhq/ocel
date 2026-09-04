import { HARNESS_PREFIX } from "../../identity";

export type Stranded = { slug: string; example: string };

export type Sweepable = { reclaim: Stranded[]; unreadable: string[] };

export function reclaimable(slug: string, examples: string[]): Stranded | undefined {
  if (!slug.startsWith(HARNESS_PREFIX)) {
    return undefined;
  }
  for (const example of [...examples].sort((a, b) => b.length - a.length)) {
    const suffix = `-${example}`;
    if (!slug.endsWith(suffix)) {
      continue;
    }
    if (slug.length - suffix.length > HARNESS_PREFIX.length) {
      return { slug, example };
    }
  }
  return undefined;
}

export function sweepable(found: string[], keep: Iterable<string>, examples: string[]): Sweepable {
  const mine = new Set(keep);
  const seen = new Set<string>();
  const reclaim: Stranded[] = [];
  const unreadable: string[] = [];
  for (const slug of found) {
    if (mine.has(slug) || seen.has(slug) || !slug.startsWith(HARNESS_PREFIX)) {
      continue;
    }
    seen.add(slug);
    const stranded = reclaimable(slug, examples);
    if (stranded) {
      reclaim.push(stranded);
    } else {
      unreadable.push(slug);
    }
  }
  return { reclaim, unreadable };
}

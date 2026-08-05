// The query-string probe (bd ocelhq-wvag.27, runbook §10.3). originUrl in
// workers/nextjs/src/index.ts builds `pathname + url.search`, while the queue
// consumer composes `origin + routePath` alone — so the fallback leg triggers a
// regeneration WITH the query and the queue leg WITHOUT it. This route exists so
// that divergence can be driven at a real prerender and the stored entry
// compared across both legs.
//
// The body carries a per-render token so a regeneration is visible at all, and
// nothing else varies: the ISR entry is keyed on routePath alone
// (`entryObjectKey`, and refreshKey = `${buildId}:${routePath}`), so if the
// query reached the stored entry it would have to show up as a second key or as
// differing bytes at the same key. Either would be the finding.
export const revalidate = 5;

export default function Page() {
  return <p id="q-token">q-token:{Date.now()}</p>;
}

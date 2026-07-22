// Preview routing: a preview worker is deployed behind a wildcard route
// (*.<baseDomain>/*), so a request's subdomain label names the deployment
// pointer to resolve — flaky-web-2626.myapp.com resolves the "flaky-web-2626"
// pointer. previewPointer extracts that label, mirroring exactly what the
// wildcard route matches: a single non-empty DNS label directly under the base
// domain. Anything else (the apex, a foreign host, a multi-label subdomain the
// wildcard would not have routed here) yields null, which the worker turns into
// its 404.
// The base domain, lowercased and stripped of surrounding dots. Empty means no
// usable base domain was configured — the signal the worker uses to decide
// preview mode is off (a malformed value degrades to normal serving rather than
// 404-ing every request).
export function normalizeBaseDomain(baseDomain: string | undefined): string {
  return (baseDomain ?? "").toLowerCase().replace(/^\.+/, "").replace(/\.+$/, "");
}

export function previewPointer(host: string, baseDomain: string): string | null {
  const h = host.toLowerCase().split(":", 1)[0];
  const base = normalizeBaseDomain(baseDomain);
  if (base === "") return null;

  const suffix = "." + base;
  if (!h.endsWith(suffix)) return null;

  const label = h.slice(0, -suffix.length);
  if (label === "" || label.includes(".")) return null;
  return label;
}

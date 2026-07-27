// Preview routing: each preview deployment gets its own exact route
// (<pointer><labelSuffix>.<baseDomain>/*), so a request's subdomain label names
// the deployment pointer to resolve. The suffix is a hash of the project slug
// and app name, which makes the hostname unique across every project and app
// sharing the zone while leaving the pointer itself human-readable —
// flaky-web-2626-a1b2c3d4e5.myapp.com resolves the "flaky-web-2626" pointer.
// previewPointer mirrors exactly what such a route matches: a single non-empty
// DNS label directly under the base domain, ending in the suffix. Anything else
// (the apex, a foreign host, a multi-label subdomain, a label belonging to
// another app) yields null, which the worker turns into its 404.

// The base domain, lowercased and stripped of surrounding dots. Empty means no
// usable base domain was configured — the signal the worker uses to decide
// preview mode is off (a malformed value degrades to normal serving rather than
// 404-ing every request).
export function normalizeBaseDomain(baseDomain: string | undefined): string {
  return (baseDomain ?? "").toLowerCase().replace(/^\.+/, "").replace(/\.+$/, "");
}

// The label suffix, lowercased. Empty means no usable suffix, so the whole label
// is the pointer; a suffix carrying a dot is unmatchable by any DNS label, so it
// degrades to empty rather than 404-ing every request.
function normalizeLabelSuffix(labelSuffix: string | undefined): string {
  const suffix = (labelSuffix ?? "").toLowerCase();
  return suffix.includes(".") ? "" : suffix;
}

export function previewPointer(
  host: string,
  baseDomain: string,
  labelSuffix: string | undefined,
): string | null {
  const h = host.toLowerCase().split(":", 1)[0];
  const base = normalizeBaseDomain(baseDomain);
  if (base === "") return null;

  const baseSuffix = "." + base;
  if (!h.endsWith(baseSuffix)) return null;

  const label = h.slice(0, -baseSuffix.length);
  if (label === "" || label.includes(".")) return null;

  const suffix = normalizeLabelSuffix(labelSuffix);
  if (!label.endsWith(suffix)) return null;

  const pointer = suffix === "" ? label : label.slice(0, -suffix.length);
  return pointer === "" ? null : pointer;
}

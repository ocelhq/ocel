// Preview routing: a project gets one entrypoint worker behind one wildcard
// route (*.<base>/*), so a request's subdomain label is the only thing naming
// which deployment of which app to serve. The label carries both:
//
//   <pointer>.<base>          the project's sole app, elided
//   <pointer>--<app>.<base>   any project
//
// The elided form is legal only where the project has exactly one app, which is
// why the worker is told its app names rather than its own identity. Everything
// else — the apex, a foreign host, a multi-label subdomain, an app the project
// does not have — yields null, which the worker turns into its 404.

// The base domain, lowercased and stripped of surrounding dots. Empty means no
// usable base domain was configured — the signal the worker uses to decide
// preview mode is off (a malformed value degrades to normal serving rather than
// 404-ing every request).
export function normalizeBaseDomain(baseDomain: string | undefined): string {
  return (baseDomain ?? "").toLowerCase().replace(/^\.+/, "").replace(/\.+$/, "");
}

// The project's app names, as the worker var carries them: comma-separated,
// lowercased to match the host, empties dropped so a trailing comma can never
// make a single-app project look like two.
export function previewApps(apps: string | undefined): string[] {
  return (apps ?? "")
    .toLowerCase()
    .split(",")
    .map((app) => app.trim())
    .filter(Boolean);
}

export interface PreviewTarget {
  pointer: string;
  app: string;
}

// A DNS label cannot carry a dot, so a doubled hyphen is what separates the two
// halves — and both halves may contain one, which is why the app half is
// resolved by matching the project's own app names rather than by splitting.
//
// Keep in step with previewAppSeparator in cloud/aws/deploy, which builds the
// hostname, and appSeparator in cli/internal/previewid, which refuses a pointer
// carrying it: three modules, no shared constant.
const APP_SEPARATOR = "--";

export function previewTarget(
  host: string,
  baseDomain: string,
  apps: string[],
): PreviewTarget | null {
  const h = host.toLowerCase().split(":", 1)[0];
  const base = normalizeBaseDomain(baseDomain);
  if (base === "") return null;

  const baseSuffix = "." + base;
  if (!h.endsWith(baseSuffix)) return null;

  const label = h.slice(0, -baseSuffix.length);
  if (label === "" || label.includes(".")) return null;

  // Left to right, so the longest app name wins where two could match
  // (apps "b" and "a--b" against the label "p--a--b").
  for (
    let at = label.indexOf(APP_SEPARATOR);
    at !== -1;
    at = label.indexOf(APP_SEPARATOR, at + 1)
  ) {
    const app = label.slice(at + APP_SEPARATOR.length);
    const pointer = label.slice(0, at);
    if (pointer !== "" && apps.includes(app)) return { pointer, app };
  }

  // No app named: the whole label is the pointer, which only a single-app
  // project can resolve.
  if (apps.length !== 1) return null;
  return { pointer: label, app: apps[0] };
}

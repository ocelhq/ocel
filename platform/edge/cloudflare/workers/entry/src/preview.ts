export function normalizeBaseDomain(baseDomain: string | undefined): string {
  return (baseDomain ?? "").toLowerCase().replace(/^\.+/, "").replace(/\.+$/, "");
}

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

  for (
    let at = label.indexOf(APP_SEPARATOR);
    at !== -1;
    at = label.indexOf(APP_SEPARATOR, at + 1)
  ) {
    const app = label.slice(at + APP_SEPARATOR.length);
    const pointer = label.slice(0, at);
    if (pointer !== "" && apps.includes(app)) return { pointer, app };
  }

  if (apps.length !== 1) return null;
  return { pointer: label, app: apps[0] };
}

export interface GlobalPreviewTarget {
  slug: string;
  pointer: string;
  app?: string;
}

export function globalPreviewTarget(
  host: string,
  baseDomain: string,
): GlobalPreviewTarget | null {
  const label = previewLabel(host, baseDomain);
  if (label === null) return null;

  const tokens = label.split(APP_SEPARATOR);
  if (tokens.length < 2 || tokens.some((token) => token === "")) return null;

  const [slug, pointer] = tokens;
  if (tokens.length === 2) return { slug, pointer };
  return { slug, pointer, app: tokens.slice(2).join(APP_SEPARATOR) };
}

function previewLabel(host: string, baseDomain: string): string | null {
  const h = host.toLowerCase().split(":", 1)[0];
  const base = normalizeBaseDomain(baseDomain);
  if (base === "") return null;

  const baseSuffix = "." + base;
  if (!h.endsWith(baseSuffix)) return null;

  const label = h.slice(0, -baseSuffix.length);
  if (label === "" || label.includes(".")) return null;
  return label;
}

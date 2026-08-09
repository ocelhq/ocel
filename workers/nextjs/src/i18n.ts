// Pages-router i18n, as Next's own router does it (server/lib/router-utils/
// resolve-routes.ts and shared/lib/i18n/get-locale-redirect.ts).
//
// The build adapter keys every localized page on its locale — /en/about, and a
// dynamic route whose sourceRegex demands a locale segment — so a request that
// names no locale has to be given one before anything can match it. The prefix
// is internal: Next never puts the default locale in a URL the client sees, and
// the one place locale detection may change the URL is the site root.
//
// @next/routing implements a normalization of its own, but it diverges from
// Next on three points that matter here — it prefixes the root as `/en/` where
// the build emits `/en`, it redirects on every path rather than only the root,
// and it knows nothing of app-router outputs, which carry no locale at all — so
// this module does the normalization and the library is handed no i18n block.

import {
  detectDomainLocale,
  getAcceptLanguageLocale,
  getCookieLocale,
  normalizeLocalePath,
  type I18nConfig,
} from "@next/routing";

export interface LocaleResolution {
  // What routing matches on: the request's pathname with the locale prefixed,
  // unless it already named one or names an output no locale owns.
  pathname: string;
  // The locale answering this request — which 404 document a miss is served
  // from, and which locale a prefixed path was found under.
  locale: string;
  // Set only at the site root, the one place Next lets a cookie or an
  // Accept-Language header change the URL.
  redirect?: URL;
}

export function resolveLocale(
  i18n: I18nConfig,
  basePath: string,
  pathnames: string[],
  url: URL,
  headers: Headers,
): LocaleResolution {
  const rest = withoutBasePath(url.pathname, basePath);
  const hadBasePath = rest !== null;
  const pathname = rest ?? url.pathname;
  const domain = detectDomainLocale(i18n.domains, url.hostname);
  const defaultLocale = domain?.defaultLocale ?? i18n.defaultLocale;

  // Next localizes neither: an asset carries no locale, and an API route is
  // reachable only under its own bare path.
  if (pathname.startsWith("/_next/") || isApiPath(pathname)) {
    return { pathname: url.pathname, locale: defaultLocale };
  }

  const { detectedLocale, pathname: bare } = normalizeLocalePath(pathname, i18n.locales);
  if (detectedLocale) return { pathname: url.pathname, locale: detectedLocale };

  // An output the build named without a locale — an app-router page, a public/
  // file — is served under that name. App pages are deliberately not localized
  // (Next's adapter skips them), so a hybrid app+pages build carries both kinds
  // and prefixing this one would route it at a page that was never emitted.
  if (pathnames.includes(url.pathname)) {
    return { pathname: url.pathname, locale: defaultLocale };
  }

  if (isRoot(bare)) {
    const redirect = rootRedirect(i18n, basePath, url, headers, domain, defaultLocale);
    if (redirect) return { pathname: url.pathname, locale: defaultLocale, redirect };
  }

  const prefixed = isRoot(bare) ? `/${defaultLocale}` : `/${defaultLocale}${bare}`;
  return {
    pathname: (hadBasePath ? basePath : "") + prefixed,
    locale: defaultLocale,
  };
}

// Which locale's 404 document answers a request that matched nothing: the one
// its path names, else the one its domain (or the config) makes default.
export function localeOf(i18n: I18nConfig, basePath: string, url: URL): string {
  const pathname = withoutBasePath(url.pathname, basePath) ?? url.pathname;
  return (
    normalizeLocalePath(pathname, i18n.locales).detectedLocale ??
    detectDomainLocale(i18n.domains, url.hostname)?.defaultLocale ??
    i18n.defaultLocale
  );
}

// The pathname under a basePath, or null when the request is not under it at
// all. Matched on a segment boundary: basePath /docs owns /docs and /docs/a,
// and has nothing to do with /documents.
function withoutBasePath(pathname: string, basePath: string): string | null {
  if (!basePath) return pathname;
  if (pathname === basePath) return "/";
  if (pathname.startsWith(`${basePath}/`)) return pathname.slice(basePath.length);
  return null;
}

function isApiPath(pathname: string): boolean {
  return pathname === "/api" || pathname.startsWith("/api/");
}

function isRoot(pathname: string): boolean {
  return pathname === "/" || pathname === "/index";
}

// getLocaleRedirect, upstream's version: only at the root, only when locale
// detection is on, and only towards a locale that is not already the default —
// so the default locale never appears in a URL.
function rootRedirect(
  i18n: I18nConfig,
  basePath: string,
  url: URL,
  headers: Headers,
  domain: ReturnType<typeof detectDomainLocale>,
  defaultLocale: string,
): URL | undefined {
  if (i18n.localeDetection === false) return undefined;

  const preferred = getAcceptLanguageLocale(
    headers.get("accept-language") ?? "",
    i18n.locales,
  );
  const detected =
    domain?.defaultLocale ??
    getCookieLocale(headers.get("cookie") ?? undefined, i18n.locales) ??
    preferred ??
    i18n.defaultLocale;

  // The preferred locale lives on a domain of its own: send the visitor there
  // rather than serving it here, and only spell the locale out when that domain
  // does not already default to it.
  const preferredDomain = detectDomainLocale(i18n.domains, undefined, preferred);
  if (domain && preferredDomain) {
    const isPreferredDomain = preferredDomain.domain === domain.domain;
    const isPreferredDefault = preferredDomain.defaultLocale === preferred;
    if (!isPreferredDomain || !isPreferredDefault) {
      const scheme = preferredDomain.http ? "http" : "https";
      const locale = isPreferredDefault ? "" : preferred;
      return new URL(`${scheme}://${preferredDomain.domain}/${locale}`);
    }
  }

  if (detected.toLowerCase() === defaultLocale.toLowerCase()) return undefined;
  const redirect = new URL(url);
  redirect.pathname = `${basePath}/${detected}`;
  return redirect;
}

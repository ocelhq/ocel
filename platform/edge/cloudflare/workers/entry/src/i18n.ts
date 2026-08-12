import {
  detectDomainLocale,
  getAcceptLanguageLocale,
  getCookieLocale,
  normalizeLocalePath,
  type I18nConfig,
} from "@next/routing";

import { dataPagePathname, withoutBasePath } from "./trailing-slash";

export interface LocaleResolution {
  pathname: string;
  redirect?: URL;
}

export function resolveLocale(
  i18n: I18nConfig,
  basePath: string,
  buildId: string,
  pathnames: string[],
  url: URL,
  headers: Headers,
): LocaleResolution {
  const dataPage = dataPagePathname(url.pathname, { basePath }, buildId);
  if (dataPage !== undefined) {
    const pageUrl = new URL(url);
    pageUrl.pathname = dataPage;
    const resolved = resolveLocale(i18n, basePath, buildId, pathnames, pageUrl, headers);
    return { pathname: wrapDataPagePathname(resolved.pathname, basePath, buildId) };
  }

  const rest = withoutBasePath(url.pathname, basePath);
  const hadBasePath = rest !== undefined;
  const pathname = rest ?? url.pathname;
  const domain = detectDomainLocale(i18n.domains, url.hostname);
  const defaultLocale = domain?.defaultLocale ?? i18n.defaultLocale;

  if (pathname.startsWith("/_next/") || isApiPath(pathname)) {
    return { pathname: url.pathname };
  }

  const { detectedLocale, pathname: bare } = normalizeLocalePath(pathname, i18n.locales);
  if (detectedLocale) return { pathname: url.pathname };

  const prefixed =
    (hadBasePath ? basePath : "") +
    (isRoot(bare) ? `/${defaultLocale}` : `/${defaultLocale}${bare}`);

  if (!pathnames.includes(prefixed) && pathnames.includes(url.pathname)) {
    return { pathname: url.pathname };
  }

  if (isRoot(bare)) {
    const redirect = rootRedirect(i18n, basePath, url, headers, domain, defaultLocale);
    if (redirect) return { pathname: url.pathname, redirect };
  }

  return { pathname: prefixed };
}

export function localeOf(i18n: I18nConfig, basePath: string, url: URL): string {
  const pathname = withoutBasePath(url.pathname, basePath) ?? url.pathname;
  return (
    normalizeLocalePath(pathname, i18n.locales).detectedLocale ??
    detectDomainLocale(i18n.domains, url.hostname)?.defaultLocale ??
    i18n.defaultLocale
  );
}

function isApiPath(pathname: string): boolean {
  return pathname === "/api" || pathname.startsWith("/api/");
}

function isRoot(pathname: string): boolean {
  return pathname === "" || pathname === "/" || pathname === "/index";
}

function wrapDataPagePathname(
  pagePathname: string,
  basePath: string,
  buildId: string,
): string {
  const rest = withoutBasePath(pagePathname, basePath) ?? pagePathname;
  const page = isRoot(rest) ? "index" : rest.slice(1);
  return `${basePath}/_next/data/${buildId}/${page}.json`;
}

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

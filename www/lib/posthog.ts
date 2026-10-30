export const INGEST_PATH = "/ingest";

const CLOUD_HOST = /^(us|eu)\.i\.posthog\.com$/;

export function posthogHosts(apiHost: string) {
  const { origin, hostname } = new URL(apiHost);
  const region = CLOUD_HOST.exec(hostname)?.[1];
  if (!region) return { ingest: origin, assets: origin, ui: origin };
  return {
    ingest: origin,
    assets: `https://${region}-assets.i.posthog.com`,
    ui: `https://${region}.posthog.com`,
  };
}

export function posthogRewrites(apiHost: string | undefined) {
  if (!apiHost) return [];
  const { ingest, assets } = posthogHosts(apiHost);
  return [
    { source: `${INGEST_PATH}/static/:path*`, destination: `${assets}/static/:path*` },
    { source: `${INGEST_PATH}/array/:path*`, destination: `${assets}/array/:path*` },
    { source: `${INGEST_PATH}/:path*`, destination: `${ingest}/:path*` },
  ];
}

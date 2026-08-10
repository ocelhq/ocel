export const isrPrefix = "prod/proj/web/BID";
export const routeId = "/";

export function body(overrides: Record<string, unknown> = {}): string {
  return JSON.stringify({
    v: 1,
    headers: { "x-prerender-revalidate": "s3cr3t-preview-mode-id", "x-forwarded-host": "example.com" },
    expect: { header: "x-nextjs-cache", value: "REVALIDATED" },
    isrPrefix,
    routeId,
    routePath: "/blog/post",
    lastModified: 1_700_000_000_000,
    enqueuedAt: 1_700_000_000_500,
    ...overrides,
  });
}

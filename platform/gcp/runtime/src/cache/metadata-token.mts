export interface MetadataTokenOptions {
  service: string;
  fetch?: typeof fetch;
  metadataOrigin?: string;
  now?: () => number;
  timeoutMs?: number;
}

export interface MetadataToken {
  token(): Promise<string>;
  forget(): void;
}

const refreshMarginMs = 60_000;

export function newMetadataToken(options: MetadataTokenOptions): MetadataToken {
  const doFetch = options.fetch ?? globalThis.fetch;
  const metadataOrigin = options.metadataOrigin ?? "http://metadata.google.internal";
  const now = options.now ?? Date.now;
  const timeoutMs = options.timeoutMs ?? 5_000;
  const url = `${metadataOrigin}/computeMetadata/v1/instance/service-accounts/default/token`;

  let cached: { value: string; refreshAt: number } | undefined;
  let inflight: Promise<string> | undefined;

  async function fetchToken(): Promise<string> {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    const unanswered = () =>
      new Error(
        `ocel: the metadata server gave no ${options.service} token within ${timeoutMs} ms`,
      );
    try {
      let res: Response;
      try {
        res = await doFetch(url, {
          headers: { "Metadata-Flavor": "Google" },
          signal: controller.signal,
        });
      } catch (error) {
        if (controller.signal.aborted) throw unanswered();
        throw new Error(
          `ocel: the metadata server could not be reached for a ${options.service} token: ${error instanceof Error ? error.message : String(error)}`,
        );
      }
      if (!res.ok) {
        throw new Error(
          `ocel: the metadata server gave no ${options.service} token: ${res.status}`,
        );
      }
      let body: { access_token?: unknown; expires_in?: unknown };
      try {
        body = ((await res.json()) as typeof body | null) ?? {};
      } catch {
        if (controller.signal.aborted) throw unanswered();
        body = {};
      }
      if (typeof body.access_token !== "string" || typeof body.expires_in !== "number") {
        throw new Error(
          `ocel: the metadata server answered with no ${options.service} token it could read`,
        );
      }
      cached = {
        value: body.access_token,
        refreshAt: now() + body.expires_in * 1000 - refreshMarginMs,
      };
      return body.access_token;
    } finally {
      clearTimeout(timer);
    }
  }

  return {
    token() {
      if (cached && now() < cached.refreshAt) return Promise.resolve(cached.value);
      inflight ??= fetchToken().finally(() => {
        inflight = undefined;
      });
      return inflight;
    },
    forget() {
      cached = undefined;
    },
  };
}

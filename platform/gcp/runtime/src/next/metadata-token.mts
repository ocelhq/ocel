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

  let cached: { value: string; refreshAt: number } | undefined;
  let inflight: Promise<string> | undefined;

  async function fetchToken(): Promise<string> {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    try {
      const res = await doFetch(
        `${metadataOrigin}/computeMetadata/v1/instance/service-accounts/default/token`,
        { headers: { "Metadata-Flavor": "Google" }, signal: controller.signal },
      );
      if (!res.ok) {
        throw new Error(
          `ocel: the metadata server gave no ${options.service} token: ${res.status}`,
        );
      }
      const body = (await res.json()) as { access_token: string; expires_in: number };
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

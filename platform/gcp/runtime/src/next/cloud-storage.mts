export interface CloudStorageOptions {
  bucket: string;
  endpoint?: string;
  fetch?: typeof fetch;
  metadataOrigin?: string;
  now?: () => number;
  sleep?: (ms: number) => Promise<void>;
  random?: () => number;
  requestTimeoutMs?: number;
}

export type ObjectRead =
  | { status: "found"; body: string; generation: string }
  | { status: "absent" }
  | { status: "unchanged" };

export type ObjectWrite = { status: "written"; generation: string } | { status: "lost" };

export interface CloudStorage {
  read(name: string, conditions?: { ifGenerationNotMatch?: string }): Promise<ObjectRead>;
  write(
    name: string,
    body: string,
    conditions?: { ifGenerationMatch?: string },
  ): Promise<ObjectWrite>;
}

const attempts = 4;
const tokenRefreshMarginMs = 60_000;

export function newCloudStorage(options: CloudStorageOptions): CloudStorage {
  const { bucket } = options;
  const origin = (options.endpoint ?? "https://storage.googleapis.com").replace(/\/$/, "");
  const doFetch = options.fetch ?? globalThis.fetch;
  const metadataOrigin = options.metadataOrigin ?? "http://metadata.google.internal";
  const now = options.now ?? Date.now;
  const sleep =
    options.sleep ?? ((ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms)));
  const random = options.random ?? Math.random;
  const requestTimeoutMs = options.requestTimeoutMs ?? 10_000;

  let cached: { value: string; refreshAt: number } | undefined;
  let inflight: Promise<string> | undefined;

  async function fetchToken(): Promise<string> {
    const res = await doFetch(
      `${metadataOrigin}/computeMetadata/v1/instance/service-accounts/default/token`,
      { headers: { "Metadata-Flavor": "Google" } },
    );
    if (!res.ok) {
      throw new Error(`ocel: the metadata server gave no Cloud Storage token: ${res.status}`);
    }
    const body = (await res.json()) as { access_token: string; expires_in: number };
    cached = {
      value: body.access_token,
      refreshAt: now() + body.expires_in * 1000 - tokenRefreshMarginMs,
    };
    return body.access_token;
  }

  function token(): Promise<string> {
    if (cached && now() < cached.refreshAt) return Promise.resolve(cached.value);
    inflight ??= fetchToken().finally(() => {
      inflight = undefined;
    });
    return inflight;
  }

  async function send(url: string, init: RequestInit): Promise<Response> {
    const headers = new Headers(init.headers);
    if (options.endpoint === undefined) headers.set("Authorization", `Bearer ${await token()}`);
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), requestTimeoutMs);
    try {
      return await doFetch(url, { ...init, headers, signal: controller.signal });
    } finally {
      clearTimeout(timer);
    }
  }

  async function request(
    verb: "read" | "write",
    name: string,
    url: string,
    init: RequestInit,
    accept: (res: Response) => Promise<unknown> | undefined,
  ): Promise<unknown> {
    const fail = (cause: string) =>
      new Error(`ocel: Cloud Storage ${verb} of ${bucket}/${name} failed: ${cause}`);
    let refreshed = false;
    let lastCause = "";
    for (let attempt = 1; attempt <= attempts; attempt++) {
      if (attempt > 1) {
        await sleep(random() * Math.min(2000, 100 * 2 ** (attempt - 2)));
      }
      let res: Response;
      try {
        res = await send(url, init);
        if (res.status === 401 && !refreshed && options.endpoint === undefined) {
          refreshed = true;
          cached = undefined;
          res = await send(url, init);
        }
      } catch (error) {
        lastCause = error instanceof Error ? error.message : String(error);
        continue;
      }
      const accepted = accept(res);
      if (accepted) return accepted;
      lastCause = String(res.status);
      const retryable = res.status === 408 || res.status === 429 || res.status >= 500;
      if (!retryable) throw fail(lastCause);
    }
    throw fail(lastCause);
  }

  return {
    async read(name, conditions) {
      const query = new URLSearchParams({ alt: "media" });
      if (conditions?.ifGenerationNotMatch !== undefined) {
        query.set("ifGenerationNotMatch", conditions.ifGenerationNotMatch);
      }
      const url = `${origin}/storage/v1/b/${bucket}/o/${encodeURIComponent(name)}?${query}`;
      return (await request("read", name, url, { method: "GET" }, (res) => {
        if (res.status === 404) return Promise.resolve<ObjectRead>({ status: "absent" });
        if (res.status === 304) return Promise.resolve<ObjectRead>({ status: "unchanged" });
        if (res.status !== 200) return undefined;
        return (async (): Promise<ObjectRead> => {
          const generation = res.headers.get("x-goog-generation");
          if (generation === null) {
            throw new Error(`ocel: Cloud Storage answered ${name} with no generation`);
          }
          return { status: "found", body: await res.text(), generation };
        })();
      })) as ObjectRead;
    },

    async write(name, body, conditions) {
      const query = new URLSearchParams({ uploadType: "media", name });
      if (conditions?.ifGenerationMatch !== undefined) {
        query.set("ifGenerationMatch", conditions.ifGenerationMatch);
      }
      const url = `${origin}/upload/storage/v1/b/${bucket}/o?${query}`;
      return (await request(
        "write",
        name,
        url,
        { method: "POST", body, headers: { "Content-Type": "application/json" } },
        (res) => {
          if (res.status === 412) return Promise.resolve<ObjectWrite>({ status: "lost" });
          if (res.status !== 200) return undefined;
          return (res.json() as Promise<{ generation: string | number }>).then(
            (json): ObjectWrite => ({
              status: "written",
              generation: String(json.generation),
            }),
          );
        },
      )) as ObjectWrite;
    },
  };
}

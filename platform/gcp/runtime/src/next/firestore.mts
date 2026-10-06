import { newMetadataToken } from "./metadata-token.mjs";

export interface FirestoreOptions {
  database: string;
  endpoint?: string;
  fetch?: typeof fetch;
  metadataOrigin?: string;
  now?: () => number;
  sleep?: (ms: number) => Promise<void>;
  random?: () => number;
  requestTimeoutMs?: number;
}

export interface FirestoreCommit {
  commitTime: string;
  writeResults: { updateTime?: string; transformResults?: Record<string, unknown>[] }[];
}

export interface FirestoreValue {
  stringValue?: string;
  integerValue?: string;
  timestampValue?: string;
}

export interface FirestoreQueryResult {
  readTime: string;
  documents: { name: string; fields: Record<string, FirestoreValue> }[];
}

export interface Firestore {
  readonly database: string;
  commit(writes: unknown[]): Promise<FirestoreCommit>;
  runQuery(structuredQuery: unknown): Promise<FirestoreQueryResult>;
}

const attempts = 4;

export class FirestoreError extends Error {
  constructor(
    message: string,
    readonly status: number | undefined,
  ) {
    super(message);
    this.name = "FirestoreError";
  }
}

export function newFirestore(options: FirestoreOptions): Firestore {
  const { database } = options;
  const origin = (options.endpoint ?? "https://firestore.googleapis.com").replace(/\/$/, "");
  const doFetch = options.fetch ?? globalThis.fetch;
  const sleep =
    options.sleep ?? ((ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms)));
  const random = options.random ?? Math.random;
  const requestTimeoutMs = options.requestTimeoutMs ?? 10_000;
  const metadataToken = newMetadataToken({
    service: "Firestore",
    fetch: doFetch,
    metadataOrigin: options.metadataOrigin,
    now: options.now,
  });

  async function send(url: string, body: unknown): Promise<Response> {
    const headers = new Headers({ "Content-Type": "application/json" });
    if (options.endpoint === undefined) {
      headers.set("Authorization", `Bearer ${await metadataToken.token()}`);
    }
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), requestTimeoutMs);
    try {
      return await doFetch(url, {
        method: "POST",
        headers,
        body: JSON.stringify(body),
        signal: controller.signal,
      });
    } finally {
      clearTimeout(timer);
    }
  }

  async function call(method: "commit" | "runQuery", body: unknown): Promise<unknown> {
    const url = `${origin}/v1/${database}/documents:${method}`;
    let refreshed = false;
    let lastCause = "";
    let lastStatus: number | undefined;
    for (let attempt = 1; attempt <= attempts; attempt++) {
      if (attempt > 1) await sleep(random() * Math.min(2000, 100 * 2 ** (attempt - 2)));
      let res: Response;
      try {
        res = await send(url, body);
        if (res.status === 401 && !refreshed && options.endpoint === undefined) {
          refreshed = true;
          metadataToken.forget();
          res = await send(url, body);
        }
        if (res.status === 200) return await res.json();
      } catch (error) {
        lastCause = error instanceof Error ? error.message : String(error);
        lastStatus = undefined;
        continue;
      }
      lastCause = String(res.status);
      lastStatus = res.status;
      const retryable =
        res.status === 408 || res.status === 409 || res.status === 429 || res.status >= 500;
      if (!retryable) break;
    }
    throw new FirestoreError(
      `ocel: Firestore ${method} on ${database} failed: ${lastCause}`,
      lastStatus,
    );
  }

  return {
    database,
    async commit(writes) {
      return (await call("commit", { writes })) as FirestoreCommit;
    },

    async runQuery(structuredQuery) {
      const elements = (await call("runQuery", { structuredQuery })) as {
        readTime?: string;
        document?: { name: string; fields?: Record<string, FirestoreValue> };
      }[];
      let readTime: string | undefined;
      const documents: FirestoreQueryResult["documents"] = [];
      for (const element of elements) {
        if (element.readTime !== undefined) readTime = element.readTime;
        if (element.document) {
          documents.push({ name: element.document.name, fields: element.document.fields ?? {} });
        }
      }
      if (readTime === undefined) {
        throw new Error(`ocel: Firestore runQuery on ${database} answered with no read time`);
      }
      return { readTime, documents };
    },
  };
}

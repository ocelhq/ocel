type Value = { stringValue?: string; integerValue?: string; timestampValue?: string };
type Fields = Record<string, Value>;

export interface FirestoreDatabaseOptions {
  database: string;
  latencyMs?: number | ((commitIndex: number) => number);
  limits?: { prefixWritesPerSecond: number; documentWritesPerSecond: number };
}

interface StoredDocument {
  fields: Fields;
  visibleAt: number;
}

interface Target {
  database: string;
  method: string;
  body: any;
}

const oneSecondMs = 1000;

export function newFirestoreDatabase(options: FirestoreDatabaseOptions) {
  const documents = new Map<string, StoredDocument>();
  const prefixWrites = new Map<string, number[]>();
  const documentWrites = new Map<string, number[]>();
  const failures: number[] = [];
  const commits: { writes: any[] }[] = [];
  let commitIndex = 0;

  function recent(log: Map<string, number[]>, key: string, at: number): number[] {
    const times = (log.get(key) ?? []).filter((t) => t > at - oneSecondMs);
    log.set(key, times);
    return times;
  }

  function wouldOverrun(writes: any[], at: number): boolean {
    const limits = options.limits;
    if (!limits) return false;
    const perPrefix = new Map<string, number>();
    const perDocument = new Map<string, number>();
    for (const write of writes) {
      const prefix = write.update.fields.prefix.stringValue as string;
      perPrefix.set(prefix, (perPrefix.get(prefix) ?? 0) + 1);
      perDocument.set(write.update.name, (perDocument.get(write.update.name) ?? 0) + 1);
    }
    for (const [prefix, count] of perPrefix) {
      if (recent(prefixWrites, prefix, at).length + count > limits.prefixWritesPerSecond) {
        return true;
      }
    }
    for (const [name, count] of perDocument) {
      if (recent(documentWrites, name, at).length + count > limits.documentWritesPerSecond) {
        return true;
      }
    }
    return false;
  }

  function apply(write: any, requestTime: string, visibleAt: number) {
    const name = write.update.name as string;
    const stored = documents.get(name) ?? { fields: {}, visibleAt };
    const fields: Fields = { ...stored.fields };
    for (const path of write.updateMask.fieldPaths as string[]) {
      fields[path] = write.update.fields[path];
    }
    const results: Value[] = [];
    for (const transform of write.updateTransforms as any[]) {
      if (transform.maximum) {
        const incoming = Number(transform.maximum.integerValue);
        const existing = fields[transform.fieldPath]?.integerValue;
        const value = String(
          existing === undefined ? incoming : Math.max(Number(existing), incoming),
        );
        fields[transform.fieldPath] = { integerValue: value };
        results.push({ integerValue: value });
      } else if (transform.setToServerValue === "REQUEST_TIME") {
        fields[transform.fieldPath] = { timestampValue: requestTime };
        results.push({ timestampValue: requestTime });
      }
    }
    documents.set(name, { fields, visibleAt: stored.visibleAt });
    return { updateTime: requestTime, transformResults: results };
  }

  async function commit(body: any, delay: (ms: number) => Promise<void>): Promise<Response> {
    const writes = body.writes as any[];
    const receivedAt = Date.now();
    if (wouldOverrun(writes, receivedAt)) return Response.json({}, { status: 409 });
    for (const write of writes) {
      const prefix = write.update.fields.prefix.stringValue as string;
      recent(prefixWrites, prefix, receivedAt).push(receivedAt);
      recent(documentWrites, write.update.name, receivedAt).push(receivedAt);
    }
    const index = commitIndex++;
    commits.push({ writes });
    const requestTime = new Date(receivedAt).toISOString();
    const latency =
      typeof options.latencyMs === "function" ? options.latencyMs(index) : (options.latencyMs ?? 0);
    if (latency > 0) await delay(latency);
    const commitMs = Date.now();
    const writeResults = writes.map((write) => apply(write, requestTime, commitMs));
    return Response.json({ commitTime: new Date(commitMs).toISOString(), writeResults });
  }

  function runQuery(body: any): Response {
    const query = body.structuredQuery;
    const readMs = Date.now();
    const filters = query.where.compositeFilter?.filters ?? [query.where];
    let prefix = "";
    let after = -Infinity;
    for (const { fieldFilter } of filters) {
      if (fieldFilter.field.fieldPath === "prefix") prefix = fieldFilter.value.stringValue;
      if (fieldFilter.field.fieldPath === "writtenAt") {
        after = Date.parse(fieldFilter.value.timestampValue);
      }
    }
    const matches = [...documents.entries()]
      .filter(
        ([, doc]) =>
          doc.visibleAt <= readMs &&
          doc.fields.prefix?.stringValue === prefix &&
          Date.parse(doc.fields.writtenAt!.timestampValue!) > after,
      )
      .sort(
        (a, b) =>
          Date.parse(a[1].fields.writtenAt!.timestampValue!) -
          Date.parse(b[1].fields.writtenAt!.timestampValue!),
      );
    const readTime = new Date(readMs).toISOString();
    return Response.json(
      matches.length === 0
        ? [{ readTime }]
        : matches.map(([name, doc]) => ({ readTime, document: { name, fields: doc.fields } })),
    );
  }

  function parse(url: string): Target | undefined {
    const match = /\/v1\/(.+)\/documents:(commit|runQuery)$/.exec(url);
    return match ? { database: match[1]!, method: match[2]!, body: undefined } : undefined;
  }

  const fetchStub = (async (input: string | URL | Request, init: RequestInit = {}) => {
    const url = String(input);
    if (url.includes("/computeMetadata/")) {
      return Response.json({ access_token: "token", expires_in: 3600 });
    }
    const target = parse(url);
    if (!target || target.database !== options.database) {
      return Response.json({}, { status: 404 });
    }
    const status = failures.shift();
    if (status !== undefined) return Response.json({}, { status });
    const body = JSON.parse(init.body as string);
    if (target.method === "commit") {
      return commit(body, (ms) => new Promise<void>((resolve) => setTimeout(resolve, ms)));
    }
    return runQuery(body);
  }) as typeof fetch;

  return {
    database: options.database,
    fetch: fetchStub,
    commits,
    documents,
    fail(count: number, status: number) {
      for (let i = 0; i < count; i++) failures.push(status);
    },
    document(name: string): Fields | undefined {
      return documents.get(name)?.fields;
    },
  };
}

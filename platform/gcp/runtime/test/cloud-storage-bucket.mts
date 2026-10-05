export interface StoredObject {
  body: string;
  generation: string;
}

export interface CloudStorageBucket {
  fetch: typeof fetch;
  objects: Map<string, StoredObject>;
  requests: { method: string; name: string; query: URLSearchParams }[];
  fail(times: number, status: number): void;
  loseNextWrites(times: number): void;
}

export function newCloudStorageBucket(): CloudStorageBucket {
  const objects = new Map<string, StoredObject>();
  const requests: CloudStorageBucket["requests"] = [];
  let failures = { times: 0, status: 0 };
  let lostWrites = 0;
  let generation = 0;

  const served: typeof fetch = async (input, init) => {
    const url = new URL(String(input));
    const method = init?.method ?? "GET";
    const upload = url.pathname.startsWith("/upload/");
    const name = upload
      ? (url.searchParams.get("name") ?? "")
      : decodeURIComponent(url.pathname.split("/o/")[1] ?? "");
    requests.push({ method, name, query: url.searchParams });

    if (failures.times > 0) {
      failures.times--;
      return new Response("", { status: failures.status });
    }

    const stored = objects.get(name);
    if (method === "GET") {
      if (!stored) return new Response("", { status: 404 });
      if (url.searchParams.get("ifGenerationNotMatch") === stored.generation) {
        return new Response(null, { status: 304 });
      }
      return new Response(stored.body, { headers: { "x-goog-generation": stored.generation } });
    }

    if (lostWrites > 0) {
      lostWrites--;
      return new Response("", { status: 412 });
    }
    const required = url.searchParams.get("ifGenerationMatch");
    const matches =
      required === null || (required === "0" ? !stored : stored?.generation === required);
    if (!matches) return new Response("", { status: 412 });
    const landed = String(++generation);
    objects.set(name, { body: String(init?.body ?? ""), generation: landed });
    return Response.json({ generation: landed });
  };

  return {
    fetch: served,
    objects,
    requests,
    fail(times, status) {
      failures = { times, status };
    },
    loseNextWrites(times) {
      lostWrites = times;
    },
  };
}

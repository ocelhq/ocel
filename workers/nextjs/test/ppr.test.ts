import { describe, expect, it } from "vitest";

import { composePpr, resumeRequest, type PprHit } from "../src/ppr";

function hit(over: Partial<PprHit> = {}): PprHit {
  return {
    kind: "ppr",
    shell: new Response("[shell]", {
      status: 200,
      headers: { "content-type": "text/html", "x-ocel-isr": "HIT" },
    }),
    postponed: "POSTPONED",
    stale: false,
    ...over,
  };
}

describe("resumeRequest", () => {
  const url = new URL("https://fn.example/blog");

  it("POSTs the postponed state as the raw body with the resume header", async () => {
    const req = resumeRequest(
      url,
      new Request("https://app.example/blog"),
      "POSTPONED",
    );

    expect(req.method).toBe("POST");
    expect(req.headers.get("next-resume")).toBe("1");
    expect(await req.text()).toBe("POSTPONED");
    expect(req.headers.get("content-length")).toBe(
      String(new TextEncoder().encode("POSTPONED").byteLength),
    );
  });

  it("carries the client's personalization headers through unfiltered", () => {
    const req = resumeRequest(
      url,
      new Request("https://app.example/blog", {
        headers: {
          cookie: "session=abc",
          authorization: "Bearer t",
          RSC: "1",
          "user-agent": "Mozilla/5.0",
        },
      }),
      "POSTPONED",
    );

    expect(req.headers.get("cookie")).toBe("session=abc");
    expect(req.headers.get("authorization")).toBe("Bearer t");
    expect(req.headers.get("RSC")).toBe("1");
    expect(req.headers.get("user-agent")).toBe("Mozilla/5.0");
  });

  it("drops hop-by-hop headers", () => {
    const req = resumeRequest(
      url,
      new Request("https://app.example/blog", {
        headers: { connection: "keep-alive", "transfer-encoding": "chunked" },
      }),
      "POSTPONED",
    );

    expect(req.headers.get("connection")).toBeNull();
    expect(req.headers.get("transfer-encoding")).toBeNull();
  });

  it("honors the pprChain headers the build declared", () => {
    const req = resumeRequest(
      url,
      new Request("https://app.example/blog"),
      "POSTPONED",
      { "next-resume": "1", "x-custom": "y" },
    );

    expect(req.headers.get("next-resume")).toBe("1");
    expect(req.headers.get("x-custom")).toBe("y");
  });
});

describe("composePpr", () => {
  it("streams the shell bytes followed by the resumed dynamic bytes", async () => {
    const resumed = Promise.resolve(new Response("[dynamic]", { status: 200 }));
    const res = composePpr(hit(), resumed);

    expect(await res.text()).toBe("[shell][dynamic]");
  });

  it("never lets the composed response be cached", () => {
    const res = composePpr(hit(), Promise.resolve(new Response("x")));
    expect(res.headers.get("cache-control")).toBe("private, no-store");
    expect(res.headers.get("content-length")).toBeNull();
  });

  it("marks a stale pair STALE and a fresh one HIT", async () => {
    const fresh = composePpr(hit(), Promise.resolve(new Response("a")));
    const stale = composePpr(hit({ stale: true }), Promise.resolve(new Response("b")));
    expect(fresh.headers.get("x-ocel-ppr")).toBe("HIT");
    expect(stale.headers.get("x-ocel-ppr")).toBe("STALE");
    await Promise.all([fresh.text(), stale.text()]);
  });

  it("truncates rather than appends an error body when the resume fails", async () => {
    const resumed = Promise.resolve(new Response("ERROR PAGE", { status: 500 }));
    const res = composePpr(hit(), resumed);

    // The shell survives; the failed dynamic half is discarded, not concatenated.
    expect(await res.text()).toBe("[shell]");
  });

  it("truncates when the resume promise rejects outright", async () => {
    const res = composePpr(hit(), Promise.reject(new Error("origin down")));
    expect(await res.text()).toBe("[shell]");
  });

  it("carries the shell's status", () => {
    const res = composePpr(
      hit({ shell: new Response("s", { status: 404 }) }),
      Promise.resolve(new Response("d")),
    );
    expect(res.status).toBe(404);
  });
});

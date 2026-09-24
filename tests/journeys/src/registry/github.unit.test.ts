import { describe, expect, it } from "bun:test";
import { type PackageVersion, reclaimed, reclaimRegistry } from "./github";

const SINCE = new Date("2026-09-24T10:00:00Z");

function version(id: number, createdAt: string, tags: string[] = []): PackageVersion {
  return { id, created_at: createdAt, metadata: { container: { tags } } };
}

describe("reclaimed", () => {
  it("deletes nothing when the run pushed nothing", () => {
    expect(reclaimed([version(1, "2026-09-24T09:59:59Z", ["sha256-old"])], SINCE)).toEqual({
      versions: [],
    });
  });

  it("deletes the versions the run pushed, untagged children among them, and no other", () => {
    expect(
      reclaimed(
        [
          version(1, "2026-09-23T08:00:00Z", ["sha256-kept"]),
          version(2, "2026-09-24T10:00:00Z", ["sha256-ours"]),
          version(3, "2026-09-24T10:04:12Z"),
        ],
        SINCE,
      ),
    ).toEqual({ versions: [2, 3] });
  });

  it("deletes the package when the run's version is the last tagged one ghcr would refuse", () => {
    expect(
      reclaimed(
        [version(1, "2026-09-23T08:00:00Z"), version(2, "2026-09-24T10:03:00Z", ["sha256-ours"])],
        SINCE,
      ),
    ).toEqual({ package: true });
  });

  it("deletes untagged versions one by one, since ghcr refuses only the last tagged one", () => {
    expect(reclaimed([version(4, "2026-09-24T10:01:00Z")], SINCE)).toEqual({ versions: [4] });
  });
});

type Asked = { method: string; url: string; authorization: string | null };

function restApi(answers: Record<string, Response>): { api: typeof fetch; asked: Asked[] } {
  const asked: Asked[] = [];
  const api = (async (input: string | URL | Request, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    asked.push({ method, url, authorization: new Headers(init?.headers).get("authorization") });
    return answers[`${method} ${url}`]?.clone() ?? new Response(null, { status: 204 });
  }) as typeof fetch;
  return { api, asked };
}

const PACKAGE = "https://api.github.com/orgs/ocelhq/packages/container/journey-vps%2Fweb";
const LISTING = `GET ${PACKAGE}/versions?per_page=100&page=1`;
const WEB = { org: "ocelhq", name: "journey-vps/web" };

describe("reclaimRegistry", () => {
  it("leaves a package no run has pushed alone", async () => {
    const { api, asked } = restApi({ [LISTING]: new Response("{}", { status: 404 }) });
    await reclaimRegistry([WEB], SINCE, "t0ken", api);
    expect(asked.map((one) => one.method)).toEqual(["GET"]);
    expect(asked[0]?.authorization).toBe("Bearer t0ken");
  });

  it("deletes the run's versions from the package the slash in its name is escaped in", async () => {
    const { api, asked } = restApi({
      [LISTING]: Response.json([
        version(1, "2026-09-23T08:00:00Z", ["sha256-kept"]),
        version(2, "2026-09-24T10:02:00Z", ["sha256-ours"]),
      ]),
    });
    await reclaimRegistry([WEB], SINCE, "t0ken", api);
    expect(asked.slice(1).map((one) => `${one.method} ${one.url}`)).toEqual([
      `DELETE ${PACKAGE}/versions/2`,
    ]);
  });

  it("deletes the whole package when the run's version is its last tagged one", async () => {
    const { api, asked } = restApi({
      [LISTING]: Response.json([version(2, "2026-09-24T10:02:00Z", ["sha256-ours"])]),
    });
    await reclaimRegistry([WEB], SINCE, "t0ken", api);
    expect(asked.slice(1).map((one) => `${one.method} ${one.url}`)).toEqual([`DELETE ${PACKAGE}`]);
  });

  it("fails loudly when the package refuses a deletion", async () => {
    const { api } = restApi({
      [LISTING]: Response.json([
        version(1, "2026-09-23T08:00:00Z", ["sha256-kept"]),
        version(2, "2026-09-24T10:02:00Z"),
      ]),
      [`DELETE ${PACKAGE}/versions/2`]: new Response('{"message":"Must have admin rights"}', {
        status: 403,
      }),
    });
    await expect(reclaimRegistry([WEB], SINCE, "t0ken", api)).rejects.toThrow(/403/);
  });
});

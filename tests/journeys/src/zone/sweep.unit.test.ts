import { describe, expect, it } from "bun:test";
import { fixtures } from "../matrix/fixtures";
import type { HttpIo } from "../retry";
import type { LookRun, Verdict } from "../targets/aws/runs";
import { CloudflareApi, type DnsRecord, type OriginCertificate } from "./cloudflare";
import { cloudflareCellsOn, journeySlugOf, sweepRunFromZone, sweepStaleFromZone } from "./sweep";

const ZONE = { id: "z1", name: "j.example" };
const CELLS = cloudflareCellsOn(fixtures, "vps");

function host(run: string, cell = "deploy-node-cloudflare", app = "web"): string {
  return `${app}-j-${run}-${cell}.${ZONE.name}`;
}

function owned(run: string, cell = "deploy-node-cloudflare"): string {
  return `managed by ocel for ocel--j-${run}-${cell}--production`;
}

function record(id: string, name: string, change: Partial<DnsRecord> = {}): DnsRecord {
  return { id, name, type: "A", proxied: true, comment: owned("111"), ...change };
}

const RECORDS: DnsRecord[] = [
  record("dead", host("111")),
  record("dead-plain-comment", host("111", "lifecycle-next-cloudflare"), {
    comment: "managed by ocel",
  }),
  record("live", host("222"), { comment: owned("222") }),
  record("unreadable", host("333"), { comment: owned("333") }),
  record("grey", host("111"), { proxied: false }),
  record("hand-made", host("111"), { comment: "pointed here by hand" }),
  record("uncommented", host("111"), { comment: null }),
  record("other-owner", host("111"), { comment: owned("999") }),
  record("local", host("local-vndaba"), { comment: owned("local-vndaba") }),
  record("unknown-app", host("111", "deploy-node-cloudflare", "api")),
  record("unknown-cell", host("111", "deploy-go-cloudflare"), {
    comment: owned("111", "deploy-go-cloudflare"),
  }),
  record("deeper", `${host("111").replace(`.${ZONE.name}`, "")}.sub.${ZONE.name}`),
  record("elsewhere", `${host("111").replace(ZONE.name, "other.example")}`),
  record("mine", host("444"), { comment: owned("444") }),
];

function requestFrom(organization: string): string {
  const printable = 0x13;
  const der = Buffer.concat([
    Buffer.from([0x30, 0x0d, 0x31, 0x0b, 0x30, 0x09, 0x06, 0x03, 0x55, 0x04, 0x0a]),
    Buffer.from([printable, organization.length]),
    Buffer.from(organization, "ascii"),
  ]);
  return `-----BEGIN CERTIFICATE REQUEST-----\n${der.toString("base64")}\n-----END CERTIFICATE REQUEST-----\n`;
}

function certificate(
  id: string,
  hostnames: string[],
  requestType = "origin-ecc",
  csr = requestFrom("ocel"),
): OriginCertificate {
  return { id, hostnames, request_type: requestType, csr };
}

const CERTIFICATES: OriginCertificate[] = [
  certificate("dead", [host("111")]),
  certificate("live", [host("222")]),
  certificate("wide", [host("111"), `www.${ZONE.name}`]),
  certificate("rsa", [host("111")], "origin-rsa"),
  certificate("apex", [`www.${ZONE.name}`]),
  certificate("mine", [host("444")]),
  certificate("theirs", [host("111")], "origin-ecc", requestFrom("acme")),
];

type Asked = { method: string; url: URL; authorization: string | null };

function cloudflare(
  answers: { records?: DnsRecord[]; certificates?: OriginCertificate[] } = {},
  override: (asked: Asked) => Response | undefined = () => undefined,
) {
  const asked: Asked[] = [];
  const slept: number[] = [];
  const api = (async (input: string | URL | Request, init?: RequestInit) => {
    const one = {
      method: init?.method ?? "GET",
      url: new URL(String(input)),
      authorization: new Headers(init?.headers).get("authorization"),
    };
    asked.push(one);
    const overridden = override(one);
    if (overridden) {
      return overridden;
    }
    if (one.method === "DELETE") {
      return Response.json({ success: true, result: { id: one.url.pathname.split("/").pop() } });
    }
    const listed = one.url.pathname.endsWith("/dns_records")
      ? (answers.records ?? RECORDS)
      : one.url.pathname.endsWith("/certificates")
        ? (answers.certificates ?? CERTIFICATES)
        : [ZONE];
    return Response.json({
      success: true,
      result: listed,
      result_info: { page: 1, total_pages: 1 },
    });
  }) as typeof fetch;
  const io: HttpIo = {
    api,
    sleep: async (ms) => {
      slept.push(ms);
    },
    now: () => 0,
    random: () => 0.5,
  };
  return { client: new CloudflareApi("t0ken", io), asked, slept };
}

const RUNS: Record<string, Verdict> = {
  "111": { state: "done" },
  "222": { state: "live" },
  "333": { state: "unknown", reason: "github answered 502" },
};

const look: LookRun = async (id) => RUNS[id] ?? { state: "done" };

function deleted(asked: Asked[]): string[] {
  return asked
    .filter((one) => one.method === "DELETE")
    .map((one) => one.url.pathname.replace("/client/v4", ""));
}

describe("journeySlugOf", () => {
  it("reads the slug out of a hostname a Cloudflare cell of the matrix names", () => {
    expect(journeySlugOf(host("111"), ZONE.name, CELLS)).toBe("j-111-deploy-node-cloudflare");
  });

  it("reads no slug out of a hostname below another label of the zone", () => {
    expect(journeySlugOf(`web.${host("111")}`, ZONE.name, CELLS)).toBeUndefined();
  });

  it("reads no slug out of a hostname no Cloudflare cell of the target serves", () => {
    expect(journeySlugOf(host("111", "deploy-node-registry"), ZONE.name, CELLS)).toBeUndefined();
  });
});

describe("sweepStaleFromZone", () => {
  it("deletes the proxied records and revokes the origin certificates ocel wrote for a finished run", async () => {
    const { client, asked } = cloudflare();
    await sweepStaleFromZone(client, ZONE, CELLS, "444", look).catch(() => {});
    expect(deleted(asked)).toEqual([
      "/zones/z1/dns_records/dead",
      "/zones/z1/dns_records/dead-plain-comment",
      "/certificates/dead",
    ]);
  });

  it("lists only the proxied records ocel wrote under a Cloudflare cell's hostnames", async () => {
    const { client, asked } = cloudflare({ records: [], certificates: [] });
    await sweepStaleFromZone(client, ZONE, CELLS, "444", look);
    const listings = asked.filter((one) => one.url.pathname.endsWith("/dns_records"));
    expect(listings.map((one) => one.url.searchParams.get("name.endswith"))).toEqual([
      "-deploy-node-cloudflare.j.example",
      "-deploy-node-cloudflare-tunnel.j.example",
      "-lifecycle-next-cloudflare.j.example",
    ]);
    for (const one of listings) {
      expect(one.url.pathname).toBe("/client/v4/zones/z1/dns_records");
      expect(one.url.searchParams.get("proxied")).toBe("true");
      expect(one.url.searchParams.get("comment.startswith")).toBe("managed by ocel");
      expect(one.url.searchParams.get("match")).toBe("all");
    }
    const certificates = asked.filter((one) => one.url.pathname.endsWith("/certificates"));
    expect(certificates.map((one) => one.url.searchParams.get("zone_id"))).toEqual(["z1"]);
    expect(new Set(asked.map((one) => one.authorization))).toEqual(new Set(["Bearer t0ken"]));
  });

  it("fails naming the run it could not read, having kept what that run wrote", async () => {
    const { client, asked } = cloudflare();
    await expect(sweepStaleFromZone(client, ZONE, CELLS, "444", look)).rejects.toThrow(
      /run 333 could not be read \(github answered 502\)/,
    );
    expect(deleted(asked)).not.toContain("/zones/z1/dns_records/unreadable");
  });

  it("asks GitHub about each run once", async () => {
    const seen: string[] = [];
    const { client } = cloudflare();
    await sweepStaleFromZone(client, ZONE, CELLS, "444", async (id) => {
      seen.push(id);
      return look(id);
    }).catch(() => {});
    expect(seen.sort()).toEqual(["111", "222", "333"]);
  });

  it("keeps going past a deletion Cloudflare refuses, and fails naming it", async () => {
    const { client, asked } = cloudflare({}, (one) =>
      one.method === "DELETE" && one.url.pathname.endsWith("/dns_records/dead")
        ? Response.json({ success: false, errors: [{ message: "no" }] }, { status: 400 })
        : undefined,
    );
    await expect(
      sweepStaleFromZone(client, ZONE, CELLS, "444", async () => ({ state: "done" })),
    ).rejects.toThrow(/dns_records\/dead answered 400/);
    expect(deleted(asked)).toContain("/certificates/dead");
  });

  it("counts a record gone before its deletion answered as deleted", async () => {
    const { client } = cloudflare({ certificates: [] }, (one) =>
      one.method === "DELETE" ? new Response(null, { status: 404 }) : undefined,
    );
    await sweepStaleFromZone(client, ZONE, CELLS, "444", async (id) =>
      id === "111" ? { state: "done" } : { state: "live" },
    );
  });
});

describe("sweepRunFromZone", () => {
  it("reaches each hostname of the run by its exact name, and asks GitHub nothing", async () => {
    const { client, asked } = cloudflare();
    await sweepRunFromZone(client, ZONE, CELLS, "111");
    const listings = asked.filter((one) => one.url.pathname.endsWith("/dns_records"));
    expect(listings.map((one) => one.url.searchParams.get("name.exact"))).toEqual([
      "web-j-111-deploy-node-cloudflare.j.example",
      "web-j-111-deploy-node-cloudflare-tunnel.j.example",
      "web-j-111-lifecycle-next-cloudflare.j.example",
    ]);
    expect(deleted(asked)).toEqual([
      "/zones/z1/dns_records/dead",
      "/zones/z1/dns_records/dead-plain-comment",
      "/certificates/dead",
    ]);
  });

  it("reclaims a local run's hostnames when that run is named", async () => {
    const { client, asked } = cloudflare();
    await sweepRunFromZone(client, ZONE, CELLS, "local-vndaba");
    expect(deleted(asked)).toEqual(["/zones/z1/dns_records/local"]);
  });
});

describe("CloudflareApi", () => {
  it("reads the zone by its name inside the account", async () => {
    const { client, asked } = cloudflare();
    expect(await client.readZone("j.example", "acc0unt")).toEqual(ZONE);
    const [one] = asked;
    expect(one?.url.pathname).toBe("/client/v4/zones");
    expect(one?.url.searchParams.get("name")).toBe("j.example");
    expect(one?.url.searchParams.get("account.id")).toBe("acc0unt");
  });

  it("refuses a zone the account does not serve", async () => {
    const { client } = cloudflare({}, (one) =>
      one.url.pathname === "/client/v4/zones"
        ? Response.json({ success: true, result: [], result_info: { total_pages: 0 } })
        : undefined,
    );
    await expect(client.readZone("j.example", "acc0unt")).rejects.toThrow(
      /no zone named j.example/,
    );
  });

  it("reads every page Cloudflare says a listing has", async () => {
    const { client, asked } = cloudflare({}, (one) => {
      if (!one.url.pathname.endsWith("/certificates")) {
        return undefined;
      }
      const page = Number(one.url.searchParams.get("page"));
      return Response.json({
        success: true,
        result: [certificate(`c${page}`, [host("111")])],
        result_info: { page, total_pages: 2 },
      });
    });
    const listed = await client.listOriginCertificates("z1");
    expect(listed.map((one) => one.id)).toEqual(["c1", "c2"]);
    expect(asked).toHaveLength(2);
  });

  it("waits out a throttle before asking again", async () => {
    let throttled = false;
    const { client, slept } = cloudflare({}, () => {
      if (throttled) {
        return undefined;
      }
      throttled = true;
      return new Response(null, { status: 429, headers: { "retry-after": "4" } });
    });
    await client.listOriginCertificates("z1");
    expect(slept).toEqual([4_000]);
  });
});

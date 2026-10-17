import { type HttpIo, LIVE_IO, sendWithRetry } from "../retry";

const API = "https://api.cloudflare.com/client/v4";
const RECORDS_PER_PAGE = 100;
const TUNNELS_PER_PAGE = 1000;

export const OCEL_RECORD_COMMENT = "managed by ocel";
export const OCEL_TUNNEL_PREFIX = "ocel-";

export type Zone = { id: string; name: string; accountId: string };

export type DnsRecord = {
  id: string;
  name: string;
  type: string;
  proxied: boolean;
  comment: string | null;
};

export type OriginCertificate = {
  id: string;
  hostnames: string[];
  request_type: string;
  csr: string;
};

export type TunnelStatus = "inactive" | "down" | "healthy" | "degraded";

export type Tunnel = {
  id: string;
  name: string;
  created_at: string;
  deleted_at: string | null;
  status: TunnelStatus;
};

export type NameFilter = { exact: string } | { endsWith: string };

type Listing<T> = { success?: boolean; result?: T[]; result_info?: { total_pages?: number } };

export class CloudflareApi {
  constructor(
    private readonly token: string,
    private readonly io: HttpIo = LIVE_IO,
  ) {}

  async readZone(name: string, accountId: string): Promise<Zone> {
    const found = await this.list<Zone>("/zones", { name, "account.id": accountId });
    const [zone] = found;
    if (!zone || found.length !== 1) {
      throw new Error(`the Cloudflare account ${accountId} has no zone named ${name}`);
    }
    return { id: zone.id, name: zone.name, accountId };
  }

  listOcelRecords(zoneId: string, name: NameFilter, proxied: boolean): Promise<DnsRecord[]> {
    return this.list<DnsRecord>(`/zones/${zoneId}/dns_records`, {
      ...("exact" in name ? { "name.exact": name.exact } : { "name.endswith": name.endsWith }),
      proxied: String(proxied),
      "comment.startswith": OCEL_RECORD_COMMENT,
      match: "all",
      per_page: String(RECORDS_PER_PAGE),
    });
  }

  listOriginCertificates(zoneId: string): Promise<OriginCertificate[]> {
    return this.list<OriginCertificate>("/certificates", { zone_id: zoneId });
  }

  listOcelTunnels(accountId: string, createdBefore: Date): Promise<Tunnel[]> {
    return this.list<Tunnel>(`/accounts/${accountId}/cfd_tunnel`, {
      include_prefix: OCEL_TUNNEL_PREFIX,
      is_deleted: "false",
      existed_at: createdBefore.toISOString(),
      per_page: String(TUNNELS_PER_PAGE),
    });
  }

  deleteRecord(zoneId: string, id: string): Promise<void> {
    return this.delete(`/zones/${zoneId}/dns_records/${id}`);
  }

  revokeOriginCertificate(id: string): Promise<void> {
    return this.delete(`/certificates/${id}`);
  }

  deleteTunnel(accountId: string, id: string): Promise<void> {
    return this.delete(`/accounts/${accountId}/cfd_tunnel/${id}`);
  }

  private send(method: string, path: string): Promise<Response> {
    return sendWithRetry(this.io, `${API}${path}`, {
      method,
      headers: { authorization: `Bearer ${this.token}` },
    });
  }

  private async list<T>(path: string, query: Record<string, string>): Promise<T[]> {
    const found: T[] = [];
    for (let page = 1; ; page++) {
      const asked = `${path}?${new URLSearchParams({ ...query, page: String(page) })}`;
      const answered = await this.send("GET", asked);
      const text = await answered.text();
      const body = (answered.ok ? JSON.parse(text) : {}) as Listing<T>;
      if (!answered.ok || !body.success || !Array.isArray(body.result)) {
        throw new Error(`GET ${asked} answered ${answered.status}: ${text}`);
      }
      found.push(...body.result);
      const pages = body.result_info?.total_pages;
      const fullPage = body.result.length >= Number(query.per_page);
      if (body.result.length === 0 || (pages === undefined ? !fullPage : page >= pages)) {
        return found;
      }
    }
  }

  private async delete(path: string): Promise<void> {
    const answered = await this.send("DELETE", path);
    if (!answered.ok && answered.status !== 404) {
      throw new Error(`DELETE ${path} answered ${answered.status}: ${await answered.text()}`);
    }
  }
}

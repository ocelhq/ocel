import { type HttpIo, LIVE_IO, sendWithRetry } from "../retry";

const API = "https://api.cloudflare.com/client/v4";
const RECORDS_PER_PAGE = 100;

export const OCEL_RECORD_COMMENT = "managed by ocel";

export type Zone = { id: string; name: string };

export type DnsRecord = {
  id: string;
  name: string;
  type: string;
  proxied: boolean;
  comment: string | null;
};

export type OriginCertificate = { id: string; hostnames: string[]; request_type: string };

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
    return { id: zone.id, name: zone.name };
  }

  listProxiedOcelRecords(zoneId: string, name: NameFilter): Promise<DnsRecord[]> {
    return this.list<DnsRecord>(`/zones/${zoneId}/dns_records`, {
      ...("exact" in name ? { "name.exact": name.exact } : { "name.endswith": name.endsWith }),
      proxied: "true",
      "comment.startswith": OCEL_RECORD_COMMENT,
      match: "all",
      per_page: String(RECORDS_PER_PAGE),
    });
  }

  listOriginCertificates(zoneId: string): Promise<OriginCertificate[]> {
    return this.list<OriginCertificate>("/certificates", { zone_id: zoneId });
  }

  deleteRecord(zoneId: string, id: string): Promise<void> {
    return this.delete(`/zones/${zoneId}/dns_records/${id}`);
  }

  revokeOriginCertificate(id: string): Promise<void> {
    return this.delete(`/certificates/${id}`);
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
      if (body.result.length === 0 || page >= (body.result_info?.total_pages ?? page)) {
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

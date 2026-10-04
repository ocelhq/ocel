import { appHostname, HARNESS_PREFIX, projectSlug, slugPart } from "../identity";
import type { Cell, Fixture, TargetName } from "../matrix/types";
import { cellsOn, fixturesOn } from "../plan";
import { type LookRun, livelyRuns, runIdOf } from "../targets/aws/runs";
import { despite } from "../targets/aws/sweeper";
import {
  type CloudflareApi,
  type DnsRecord,
  type NameFilter,
  OCEL_RECORD_COMMENT,
  type OriginCertificate,
  type Zone,
} from "./cloudflare";

const OWNER_SEPARATOR = " for ";
const OWNER_FIELD_SEPARATOR = "--";
const OCEL_CERTIFICATE_TYPE = "origin-ecc";
const OCEL_CERTIFICATE_ORGANIZATION = "ocel";
const ORGANIZATION_ATTRIBUTE = [0x06, 0x03, 0x55, 0x04, 0x0a];
const STRING_TYPES = [0x0c, 0x13];

type Leftovers = {
  records: { record: DnsRecord; slug: string }[];
  certificates: { certificate: OriginCertificate; slug: string }[];
};

export function cloudflareCellsOn(fixtures: Fixture[], target: TargetName): Cell[] {
  return fixturesOn(fixtures, target)
    .flatMap((fixture) => cellsOn(fixture, target))
    .filter((cell) => cell.variant.config.edge === "cloudflare");
}

export function journeySlugOf(hostname: string, zone: string, cells: Cell[]): string | undefined {
  const label = hostname.endsWith(`.${zone}`) ? hostname.slice(0, -(zone.length + 1)) : "";
  if (label === "" || label.includes(".")) {
    return undefined;
  }
  for (const cell of cells) {
    const tail = `-${slugPart(cell.name)}`;
    for (const app of cell.fixture.apps) {
      const head = `${app}-${HARNESS_PREFIX}`;
      if (
        label.startsWith(head) &&
        label.endsWith(tail) &&
        label.length > head.length + tail.length
      ) {
        return label.slice(app.length + 1);
      }
    }
  }
  return undefined;
}

function isWrittenFor(record: DnsRecord, slug: string): boolean {
  if (!record.proxied || record.comment === null) {
    return false;
  }
  if (record.comment === OCEL_RECORD_COMMENT) {
    return true;
  }
  const owner = record.comment.startsWith(OCEL_RECORD_COMMENT + OWNER_SEPARATOR)
    ? record.comment.slice(OCEL_RECORD_COMMENT.length + OWNER_SEPARATOR.length)
    : "";
  return owner.split(OWNER_FIELD_SEPARATOR).includes(slug);
}

function isRequestedByOcel(csr: string): boolean {
  const der = Buffer.from(csr.replace(/-----[^-]+-----/g, "").replace(/\s+/g, ""), "base64");
  return STRING_TYPES.some((type) =>
    der.includes(
      Buffer.concat([
        Buffer.from(ORGANIZATION_ATTRIBUTE),
        Buffer.from([type, OCEL_CERTIFICATE_ORGANIZATION.length]),
        Buffer.from(OCEL_CERTIFICATE_ORGANIZATION, "ascii"),
      ]),
    ),
  );
}

function leftoversOf(
  records: DnsRecord[],
  certificates: OriginCertificate[],
  zone: Zone,
  cells: Cell[],
  belongs: (slug: string) => boolean,
): Leftovers {
  const found: Leftovers = { records: [], certificates: [] };
  for (const record of records) {
    const slug = journeySlugOf(record.name, zone.name, cells);
    if (slug !== undefined && belongs(slug) && isWrittenFor(record, slug)) {
      found.records.push({ record, slug });
    }
  }
  for (const certificate of certificates) {
    const [hostname] = certificate.hostnames;
    if (
      hostname === undefined ||
      certificate.hostnames.length !== 1 ||
      certificate.request_type !== OCEL_CERTIFICATE_TYPE ||
      !isRequestedByOcel(certificate.csr)
    ) {
      continue;
    }
    const slug = journeySlugOf(hostname, zone.name, cells);
    if (slug !== undefined && belongs(slug)) {
      found.certificates.push({ certificate, slug });
    }
  }
  return found;
}

async function listRecordsNamed(
  api: CloudflareApi,
  zone: Zone,
  names: NameFilter[],
): Promise<DnsRecord[]> {
  const byId = new Map<string, DnsRecord>();
  for (const name of names) {
    for (const record of await api.listProxiedOcelRecords(zone.id, name)) {
      byId.set(record.id, record);
    }
  }
  return [...byId.values()];
}

async function reclaim(
  api: CloudflareApi,
  zone: Zone,
  leftovers: Leftovers,
  complaints: string[],
): Promise<void> {
  for (const { record } of leftovers.records) {
    await despite(complaints, `the ${record.type} record ${record.name}`, async () => {
      await api.deleteRecord(zone.id, record.id);
      process.stdout.write(`swept the ${record.type} record ${record.name}\n`);
    });
  }
  for (const { certificate } of leftovers.certificates) {
    const covering = certificate.hostnames.join(", ");
    await despite(
      complaints,
      `the origin certificate ${certificate.id} for ${covering}`,
      async () => {
        await api.revokeOriginCertificate(certificate.id);
        process.stdout.write(`revoked the origin certificate ${certificate.id} for ${covering}\n`);
      },
    );
  }
}

function report(zone: Zone, complaints: string[]): void {
  if (complaints.length > 0) {
    throw new Error(`the ${zone.name} sweep left work behind:\n${complaints.join("\n")}`);
  }
}

export async function sweepStaleFromZone(
  api: CloudflareApi,
  zone: Zone,
  cells: Cell[],
  runId: string,
  look: LookRun,
): Promise<void> {
  const parts = [...new Set(cells.map((cell) => slugPart(cell.name)))];
  const records = await listRecordsNamed(
    api,
    zone,
    parts.map((part) => ({ endsWith: `-${part}.${zone.name}` })),
  );
  const certificates = await api.listOriginCertificates(zone.id);
  const stale = (slug: string) => {
    const id = runIdOf(slug);
    return id !== undefined && id !== runId;
  };
  const found = leftoversOf(records, certificates, zone, cells, stale);

  const slugs = [...found.records, ...found.certificates].map((one) => one.slug);
  const { keep, unreadable } = await livelyRuns(
    slugs.flatMap((slug) => runIdOf(slug) ?? []),
    look,
  );
  const complaints = unreadable.map(
    ({ id, reason }) =>
      `run ${id} could not be read (${reason}), so what it wrote in ${zone.name} was kept`,
  );
  const dead = (one: { slug: string }) => !keep.has(runIdOf(one.slug) ?? "");
  await reclaim(
    api,
    zone,
    { records: found.records.filter(dead), certificates: found.certificates.filter(dead) },
    complaints,
  );
  report(zone, complaints);
}

export async function sweepRunFromZone(
  api: CloudflareApi,
  zone: Zone,
  cells: Cell[],
  runId: string,
): Promise<void> {
  const slugs = new Set(cells.map((cell) => projectSlug(cell.name, runId)));
  const hostnames = cells.flatMap((cell) =>
    cell.fixture.apps.flatMap(
      (app) => appHostname(app, projectSlug(cell.name, runId), zone.name) ?? [],
    ),
  );
  const records = await listRecordsNamed(
    api,
    zone,
    hostnames.map((exact) => ({ exact })),
  );
  const certificates = await api.listOriginCertificates(zone.id);
  const complaints: string[] = [];
  await reclaim(
    api,
    zone,
    leftoversOf(records, certificates, zone, cells, (slug) => slugs.has(slug)),
    complaints,
  );
  report(zone, complaints);
}

import { HOSTNAME_EDGES } from "../config";
import { appHostname } from "../identity";
import type { CellUnderTest } from "../run/cellRun";

export function hostnameUrls(
  cell: CellUnderTest,
  zone: string | undefined,
): Map<string, string> | undefined {
  const { edge } = cell.variant.config;
  if (edge === undefined || !HOSTNAME_EDGES.includes(edge)) {
    return undefined;
  }
  if (!zone) {
    throw new Error(
      `${cell.name} is fronted by ${edge}, which answers only a hostname in a zone the run's Cloudflare token writes, and OCEL_E2E_ZONE names none`,
    );
  }
  return new Map(
    cell.fixture.apps.map((app) => {
      const hostname = appHostname(app, cell.slug, zone);
      if (!hostname) {
        throw new Error(`${app} has no hostname on ${zone}`);
      }
      return [app, `https://${hostname}`];
    }),
  );
}

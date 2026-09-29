import { appHostname } from "../identity";
import type { CellUnderTest } from "../run/cellRun";

export function cloudflareUrls(
  cell: CellUnderTest,
  zone: string | undefined,
): Map<string, string> | undefined {
  if (cell.variant.config.edge !== "cloudflare") {
    return undefined;
  }
  if (!zone) {
    throw new Error(
      `${cell.name} is fronted by Cloudflare, which answers only a hostname in a zone it serves, and OCEL_JOURNEY_ZONE names none`,
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

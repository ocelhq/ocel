import type { Fixture } from "../matrix/types";
import { sanitize } from "../naming";

const GHCR = "ghcr.io";

export type Package = { org: string; name: string };

function packageOf(server: string, app: string): Package {
  const [host, org, ...namespace] = server.split("/");
  if (host !== GHCR || !org) {
    throw new Error(`${server} is no ghcr.io namespace, and only ghcr packages are reclaimed here`);
  }
  return { org, name: [...namespace, sanitize(app)].join("/") };
}

export function registryPackages(fixtures: Fixture[]): Package[] {
  const named = new Map<string, Package>();
  for (const one of fixtures) {
    for (const variant of Object.values(one.on).flat()) {
      const server = variant?.config.registry?.server;
      if (!server) {
        continue;
      }
      for (const app of one.apps) {
        const pkg = packageOf(server, app);
        named.set(`${pkg.org}/${pkg.name}`, pkg);
      }
    }
  }
  return [...named.values()];
}

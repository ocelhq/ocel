import { cellName, type Fixture, type TargetName } from "../matrix/types";
import { sanitize } from "../naming";
import { cellApp } from "../plan";
import type { StepResult } from "../run/results";
import { DEPLOY } from "../steps";

const GHCR = "ghcr.io";

export type Package = { org: string; name: string; deployed: boolean };

function packageOf(server: string, app: string): Omit<Package, "deployed"> {
  const [host, org, ...namespace] = server.split("/");
  if (host !== GHCR || !org) {
    throw new Error(`${server} is no ghcr.io namespace, and only ghcr packages are reclaimed here`);
  }
  return { org, name: [...namespace, sanitize(app)].join("/") };
}

export function registryPackages(
  fixtures: Fixture[],
  target: TargetName,
  results: StepResult[] = [],
): Package[] {
  const deployedCells = new Set(
    results
      .filter((one) => one.title === DEPLOY && one.outcome === "passed")
      .map((one) => one.cell),
  );
  const named = new Map<string, Package>();
  for (const one of fixtures) {
    for (const variant of one.on[target] ?? []) {
      const server = variant.config.registry?.server;
      if (!server) {
        continue;
      }
      for (const app of one.apps) {
        const pkg = packageOf(server, app);
        const key = `${pkg.org}/${pkg.name}`;
        const deployed = deployedCells.has(cellApp(cellName(one, variant), app));
        named.set(key, { ...pkg, deployed: deployed || (named.get(key)?.deployed ?? false) });
      }
    }
  }
  return [...named.values()];
}

import path from "node:path";
import type { FixtureSpec } from "./spec";

function nested(fixture: FixtureSpec): boolean {
  return fixture.apps.length > 1;
}

export function appPath(fixture: FixtureSpec, app: string): string {
  return nested(fixture) ? `apps/${app}` : ".";
}

export function appFolder(fixture: FixtureSpec, app: string): string | undefined {
  return fixture.kind === "workspace" ? `/${app}` : undefined;
}

export function appHomes(fixture: FixtureSpec): string[] {
  return nested(fixture) ? fixture.apps.map((app) => appPath(fixture, app)) : [];
}

export function appCommand(fixture: FixtureSpec, app: string): string[] {
  return ["pnpm", "--dir", appPath(fixture, app), "run", "dev"];
}

export async function setAppNames(
  fixture: FixtureSpec,
  run: (name: string, args: string[]) => Promise<unknown>,
): Promise<void> {
  for (const app of fixture.apps) {
    const folder = appFolder(fixture, app);
    if (folder) {
      await run(`env-app-${app}`, ["env", "set", "APP_NAME", app, "--folder", folder]);
    }
  }
}

export function migrateCommand(): string[] {
  return ["pnpm", "run", "migrate"];
}

export function stateComplaint(home: string, withState: string[]): string | undefined {
  const here = (dir: string) => path.resolve(dir) === path.resolve(home);
  const stray = withState.filter((dir) => !here(dir));
  if (stray.length > 0) {
    return `${stray.join(", ")} hold .ocel state, and a project keeps its state, its console binding and its dev lock under the directory its config sits in (${home})`;
  }
  if (!withState.some(here)) {
    return `${home} holds no .ocel state after the project came up`;
  }
  return undefined;
}

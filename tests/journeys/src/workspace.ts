import path from "node:path";
import type { ExampleSpec } from "./spec";

function nested(example: ExampleSpec): boolean {
  return example.apps.length > 1;
}

export function appPath(example: ExampleSpec, app: string): string {
  return nested(example) ? `apps/${app}` : ".";
}

export function appFolder(example: ExampleSpec, app: string): string | undefined {
  return example.kind === "workspace" ? `/${app}` : undefined;
}

export function appHomes(example: ExampleSpec): string[] {
  return nested(example) ? example.apps.map((app) => appPath(example, app)) : [];
}

export function appCommand(example: ExampleSpec, app: string): string[] {
  return ["pnpm", "--dir", appPath(example, app), "run", "dev"];
}

export async function setAppNames(
  example: ExampleSpec,
  run: (name: string, args: string[]) => Promise<unknown>,
): Promise<void> {
  for (const app of example.apps) {
    const folder = appFolder(example, app);
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

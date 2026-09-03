import path from "node:path";
import type { ExampleSpec } from "./spec";

export function appPath(example: ExampleSpec, app: string): string {
  return example.kind === "workspace" ? `../${app}` : ".";
}

export function appFolder(example: ExampleSpec, app: string): string | undefined {
  return example.kind === "workspace" ? `/${app}` : undefined;
}

export function siblingDirs(example: ExampleSpec): string[] {
  return example.kind === "workspace" ? example.apps : [];
}

export function appCommand(example: ExampleSpec, app: string): string[] {
  return ["pnpm", "--dir", appPath(example, app), "run", "start"];
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

export function migrateCommand(example: ExampleSpec): string[] {
  const [first] = example.apps;
  if (!first) {
    throw new Error(`${example.name} declares no app to migrate through`);
  }
  return ["pnpm", "--dir", appPath(example, first), "run", "migrate"];
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

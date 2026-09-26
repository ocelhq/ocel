import { existsSync, readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { stripVTControlCharacters } from "node:util";
import { repoRoot } from "../paths";

export const FRONT_ENV = "OCEL_VPS_FRONT";

export const frontsDir = path.join(repoRoot, "tests", "fronts");

export type FrontStep = "up.sh" | "check.sh" | "down.sh";

export type Front = { name: string; dir: string; proxy: unknown };

function fronts(dir: string): string[] {
  if (!existsSync(dir)) {
    return [];
  }
  return readdirSync(dir, { withFileTypes: true })
    .filter((entry) => entry.isDirectory())
    .map((entry) => entry.name)
    .sort();
}

export function frontNamed(env: NodeJS.ProcessEnv, dir = frontsDir): Front | undefined {
  const name = env[FRONT_ENV]?.trim();
  if (!name) {
    return undefined;
  }
  const held = fronts(dir);
  if (!held.includes(name)) {
    throw new Error(
      `${FRONT_ENV}=${name} names no front under ${dir}; the fronts there are ${held.join(", ") || "none"}`,
    );
  }
  const at = path.join(dir, name);
  const read = JSON.parse(readFileSync(path.join(at, "front.json"), "utf8")) as { proxy?: unknown };
  if (read.proxy === undefined) {
    throw new Error(
      `${path.join(at, "front.json")} names no "proxy" for the projects on its box to carry`,
    );
  }
  return { name, dir: at, proxy: read.proxy };
}

const portHolders: Record<string, string> = {
  nginx: "nginx holds :80 and :443",
  "nginx-container": "container ocel-front-nginx publishes :80 and :443",
};

export function holderOf(front: Front): string {
  const holder = portHolders[front.name];
  if (holder === undefined) {
    throw new Error(
      `the journey knows nothing that holds :80 and :443 on the ${front.name} front, so it cannot check the refusal a bootstrap under ocel's own proxy gives there; name it in portHolders`,
    );
  }
  return holder;
}

export function unrefused(code: number | null, said: string, holder: string): string | undefined {
  if (code === 0) {
    return `a bootstrap under ocel's own proxy went ahead over a box where ${holder}`;
  }
  if (stripVTControlCharacters(said).includes(holder)) {
    return undefined;
  }
  return `a bootstrap under ocel's own proxy was refused without saying "${holder}":\n${said}`;
}

export function frontStep(front: Front, step: FrontStep): string {
  return readFileSync(path.join(front.dir, step), "utf8");
}

export function coveredNames(zone: string): string[] {
  return [`*.${zone}`];
}

export function stepCommand(names: string[]): string {
  return ["sudo sh -s --", ...names.map((name) => `'${name.replaceAll("'", `'\\''`)}'`)].join(" ");
}

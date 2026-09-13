import type { Stored } from "@console/connectors";
import type {
  Deployment,
  DeploymentTopology,
  DeploymentVariable,
  EnvironmentClass,
} from "@console/db/schema";
import type {
  Ability,
  AppResolution,
  Cell,
  Class,
  MatrixCell,
  MatrixRow,
  Override,
  State,
  VariableGroup,
} from "@ui/vars";

type Declared = DeploymentVariable & { folders: Set<string>; scopes: Set<string> };

function classOf(held: DeploymentVariable["class"]): Class {
  return held === "sensitive" || held === "secret" ? held : "plain";
}

function declaredOf(topology: DeploymentTopology): Map<string, Declared> {
  const out = new Map<string, Declared>();
  for (const app of topology.apps) {
    const at = app.folder ?? "";
    for (const held of app.variables) {
      if (held.class === "derived") {
        continue;
      }
      const known = out.get(held.key) ?? {
        ...held,
        folders: new Set<string>(),
        scopes: new Set<string>(),
      };
      known.folders.add(at);
      if (held.folder !== undefined) {
        known.scopes.add(held.folder);
      }
      known.required ||= held.required;
      out.set(held.key, known);
    }
  }
  return out;
}

function foldersOf(topology: DeploymentTopology, declared: Map<string, Declared>): string[] {
  const out = new Set<string>([""]);
  for (const app of topology.apps) {
    out.add(app.folder ?? "");
  }
  for (const held of declared.values()) {
    for (const folder of [...held.folders, ...held.scopes]) {
      out.add(folder);
    }
  }
  return [...out].sort((a, b) => (a === "" ? -1 : b === "" ? 1 : a.localeCompare(b)));
}

function appsOf(topology: DeploymentTopology): AppResolution[] {
  return topology.apps.map((app) => ({ name: app.name, folder: app.folder ?? "" }));
}

interface Held {
  set: boolean;
  version: number;
  reference?: Stored["reference"];
}

function keyOf(at: Cell & { environment: string }): string {
  return `${at.key} ${at.folder} ${at.environment}`;
}

function storedOf(stored: readonly Stored[]): Map<string, Held> {
  const out = new Map<string, Held>();
  for (const value of stored) {
    out.set(keyOf(value), {
      set: true,
      version: value.version,
      ...(value.reference && { reference: value.reference }),
    });
  }
  return out;
}

function overridesOf(
  key: string,
  folder: string,
  stored: readonly Stored[],
  environments: readonly string[],
): Override[] {
  return stored
    .filter((value) => value.key === key && value.folder === folder && value.environment !== "")
    .map((value) => ({
      environment: value.environment,
      version: value.version,
      orphaned: !environments.includes(value.environment),
      ...(value.reference && { reference: value.reference }),
    }));
}

function stateOfCell(folder: string, declared: Declared | undefined): MatrixCell["state"] {
  if (declared === undefined) {
    return "forbidden";
  }
  if (declared.scopes.size > 0) {
    if (!declared.scopes.has(folder)) {
      return "forbidden";
    }
  } else if (folder !== "") {
    return "optional";
  }
  return declared.required === true ? "required" : "optional";
}

function cellOf(
  key: string,
  folder: string,
  declared: Declared | undefined,
  held: Map<string, Held>,
  stored: readonly Stored[],
  environments: readonly string[],
): MatrixCell {
  const found = held.get(keyOf({ key, folder, environment: "" }));
  const overrides = overridesOf(key, folder, stored, environments);
  return {
    folder,
    state: stateOfCell(folder, declared),
    set: found?.set === true,
    version: found?.version ?? 0,
    ...(overrides.length > 0 && { overrides }),
    ...(found?.reference && { reference: found.reference }),
  };
}

export function matrixOf(
  topology: DeploymentTopology,
  stored: readonly Stored[],
  environments: readonly string[],
): State["matrix"] {
  const declared = declaredOf(topology);
  const columns = foldersOf(topology, declared);
  const held = storedOf(stored);

  const keys = new Set<string>(declared.keys());
  for (const value of stored) {
    keys.add(value.key);
  }

  const rows: MatrixRow[] = [...keys].sort().map((key) => {
    const held_key = declared.get(key);
    return {
      key,
      class: held_key ? classOf(held_key.class) : "plain",
      ...(held_key?.description && { description: held_key.description }),
      ...(held_key && held_key.scopes.size > 0 ? { scope: [...held_key.scopes] } : {}),
      ...(held_key?.group && { group: held_key.group }),
      cells: columns.map((folder) => cellOf(key, folder, held_key, held, stored, environments)),
    };
  });

  const groups: VariableGroup[] = (topology.variableGroups ?? []).map((group) => ({
    key: group.key,
    required: group.required,
    ...(group.description && { description: group.description }),
  }));

  return { columns, rows, groups, apps: appsOf(topology) };
}

export function stateOf(
  slug: string,
  held: EnvironmentClass,
  topology: DeploymentTopology,
  stored: readonly Stored[],
  environments: readonly string[],
  can: Ability,
  values: "live" | "unknown" = "live",
): State {
  return {
    slug,
    tier: held,
    other: held === "production" ? "preview" : "production",
    values,
    can,
    environments: [...environments],
    matrix: matrixOf(topology, stored, held === "preview" ? environments : []),
  };
}

export type Latest = Pick<
  Deployment,
  | "id"
  | "topology"
  | "deployedAt"
  | "promotionId"
  | "providerName"
  | "providerRegion"
  | "target"
  | "tag"
>;

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
  EnvSource,
  MatrixCell,
  MatrixRow,
  Override,
  State,
  UndeclaredCell,
  VariableGroup,
} from "@ui/vars";
import { envSourceGroup } from "@ui/vars/model";

type Declared = DeploymentVariable & { folders: Set<string>; scopes: Set<string> };

function classOf(kind: DeploymentVariable["class"]): Class {
  return kind === "sensitive" || kind === "secret" ? kind : "plain";
}

function declaredOf(topology: DeploymentTopology): Map<string, Declared> {
  const out = new Map<string, Declared>();
  for (const app of topology.apps) {
    const at = app.folder ?? "";
    for (const variable of app.variables) {
      if (variable.class === "derived") {
        continue;
      }
      const known = out.get(variable.key) ?? {
        ...variable,
        folders: new Set<string>(),
        scopes: new Set<string>(),
      };
      known.folders.add(at);
      if (variable.folder !== undefined) {
        known.scopes.add(variable.folder);
      }
      known.required ||= variable.required;
      out.set(variable.key, known);
    }
  }
  return out;
}

function foldersOf(topology: DeploymentTopology, declared: Map<string, Declared>): string[] {
  const out = new Set<string>([""]);
  for (const app of topology.apps) {
    out.add(app.folder ?? "");
  }
  for (const variable of declared.values()) {
    for (const folder of [...variable.folders, ...variable.scopes]) {
      out.add(folder);
    }
  }
  return [...out].sort((a, b) => (a === "" ? -1 : b === "" ? 1 : a.localeCompare(b)));
}

function appsOf(topology: DeploymentTopology): AppResolution[] {
  return topology.apps.map((app) => ({ name: app.name, folder: app.folder ?? "" }));
}

interface StoredCell {
  set: boolean;
  version: number;
  reference?: Stored["reference"];
  envSource?: string;
}

function keyOf(at: Cell & { environment: string }): string {
  return `${at.key} ${at.folder} ${at.environment}`;
}

function storedOf(stored: readonly Stored[]): Map<string, StoredCell> {
  const out = new Map<string, StoredCell>();
  for (const value of stored) {
    out.set(keyOf(value), {
      set: true,
      version: value.version,
      ...(value.reference && { reference: value.reference }),
      ...(copiedFromEnvSource(value) && { envSource: value.envSource }),
    });
  }
  return out;
}

function copiedFromEnvSource(value: Stored): boolean {
  return value.environment === "" && value.envSource !== undefined && value.envSource !== "builtin";
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
  recorded: Map<string, StoredCell>,
  stored: readonly Stored[],
  environments: readonly string[],
): MatrixCell {
  const found = recorded.get(keyOf({ key, folder, environment: "" }));
  const overrides = overridesOf(key, folder, stored, environments);
  return {
    folder,
    state: stateOfCell(folder, declared),
    set: found?.set === true,
    version: found?.version ?? 0,
    ...(overrides.length > 0 && { overrides }),
    ...(found?.reference && { reference: found.reference }),
    ...(found?.envSource && { envSource: found.envSource }),
  };
}

function withCredentials(
  declared: Map<string, Declared>,
  envSource: EnvSource | undefined,
): Map<string, Declared> {
  const out = new Map(declared);
  for (const key of envSource?.credentials ?? []) {
    if (out.has(key)) {
      continue;
    }
    out.set(key, {
      key,
      class: "secret",
      required: true,
      group: envSourceGroup,
      description: `Read by ocel alone to log in to ${envSource?.id}; never handed to an app.`,
      folders: new Set([""]),
      scopes: new Set(),
    });
  }
  return out;
}

function undeclaredOf(
  declared: Map<string, Declared>,
  stored: readonly Stored[],
): UndeclaredCell[] {
  return stored
    .filter((value) => copiedFromEnvSource(value) && !declared.has(value.key))
    .map((value) => ({ key: value.key, folder: value.folder, envSource: value.envSource ?? "" }))
    .sort((a, b) => a.key.localeCompare(b.key) || a.folder.localeCompare(b.folder));
}

export function matrixOf(
  topology: DeploymentTopology,
  stored: readonly Stored[],
  environments: readonly string[],
  envSource?: EnvSource,
): State["matrix"] {
  const declared = withCredentials(declaredOf(topology), envSource);
  const columns = foldersOf(topology, declared);
  const recorded = storedOf(stored);
  const undeclared = undeclaredOf(declared, stored);
  const undeclaredKeys = new Set(
    undeclared
      .map((cell) => cell.key)
      .filter((key) =>
        stored.every(
          (value) => value.key !== key || (copiedFromEnvSource(value) && !declared.has(key)),
        ),
      ),
  );

  const keys = new Set<string>(declared.keys());
  for (const value of stored) {
    if (!undeclaredKeys.has(value.key)) {
      keys.add(value.key);
    }
  }

  const rows: MatrixRow[] = [...keys].sort().map((key) => {
    const variable = declared.get(key);
    return {
      key,
      class: variable ? classOf(variable.class) : "plain",
      ...(variable?.description && { description: variable.description }),
      ...(variable && variable.scopes.size > 0 ? { scope: [...variable.scopes] } : {}),
      ...(variable?.group && { group: variable.group }),
      cells: columns.map((folder) => cellOf(key, folder, variable, recorded, stored, environments)),
    };
  });

  const groups: VariableGroup[] = (topology.variableGroups ?? []).map((group) => ({
    key: group.key,
    required: group.required,
    ...(group.description && { description: group.description }),
  }));
  if (envSource !== undefined && rows.some((row) => row.group === envSourceGroup)) {
    groups.push({
      key: envSourceGroup,
      required: true,
      description: `How ocel logs in to ${envSource.id}.`,
    });
  }

  return {
    columns,
    rows,
    groups,
    apps: appsOf(topology),
    ...(undeclared.length > 0 && { undeclared }),
  };
}

export function stateOf(
  slug: string,
  environmentClass: EnvironmentClass,
  topology: DeploymentTopology,
  stored: readonly Stored[],
  environments: readonly string[],
  can: Ability,
  values: "live" | "unknown" = "live",
  envSource?: EnvSource,
): State {
  return {
    slug,
    tier: environmentClass,
    other: environmentClass === "production" ? "preview" : "production",
    values,
    can,
    environments: [...environments],
    matrix: matrixOf(
      topology,
      stored,
      environmentClass === "preview" ? environments : [],
      envSource,
    ),
    ...(envSource && { envSource }),
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

export type CellState = "required" | "optional" | "forbidden";

export interface Cell {
  key: string;
  folder: string;
}

export interface Address extends Cell {
  environment: string;
}

export interface Override {
  environment: string;
  version: number;
  orphaned?: boolean;
}

export interface MatrixCell {
  folder: string;
  state: CellState;
  set: boolean;
  version: number;
  overrides?: Override[];
  problem?: string;
}

export interface MatrixRow {
  key: string;
  class: string;
  scope?: string[];
  cells: MatrixCell[];
}

export interface AppResolution {
  name: string;
  folder: string;
  missing?: Cell[];
}

export interface State {
  slug: string;
  tier: string;
  environments: string[];
  matrix: {
    columns: string[];
    rows: MatrixRow[];
    apps: AppResolution[];
  };
}

export interface Version {
  version: number;
  createdAt: number;
  size: number;
}

export function sameAddress(a: Address, b: Address): boolean {
  return (
    a.key === b.key && a.folder === b.folder && a.environment === b.environment
  );
}

export function overrideOf(
  cell: MatrixCell,
  environment: string,
): Override | undefined {
  return cell.overrides?.find((held) => held.environment === environment);
}

export function held(
  cell: MatrixCell,
  environment: string,
): { set: boolean; version: number } {
  if (environment === "") return { set: cell.set, version: cell.version };
  const override = overrideOf(cell, environment);
  return { set: override !== undefined, version: override?.version ?? 0 };
}

export function addressable(current: State, cell: MatrixCell): string[] {
  const environments = [...current.environments];
  for (const override of cell.overrides ?? []) {
    if (!environments.includes(override.environment)) {
      environments.push(override.environment);
    }
  }
  return ["", ...environments];
}

export function cellOf(row: MatrixRow, folder: string): MatrixCell | undefined {
  return row.cells.find((cell) => cell.folder === folder);
}

export function owedCount(current: State): number {
  return current.matrix.rows.reduce(
    (total, row) =>
      total +
      row.cells.filter(
        (cell) => (cell.state === "required" && !cell.set) || cell.problem,
      ).length,
    0,
  );
}

export type SocketState = "forbidden" | "faulty" | "held" | "owed" | "free";

export function socketState(cell: MatrixCell): SocketState {
  if (cell.state === "forbidden") return "forbidden";
  if (cell.problem) return "faulty";
  if (cell.set) return "held";
  return cell.state === "required" ? "owed" : "free";
}

export function folderName(folder: string): string {
  return folder === "" ? "root" : folder;
}

export function environmentName(environment: string): string {
  return environment === "" ? "all environments" : environment;
}

const namedLimit = 5;

export function names(items: string[]): string {
  const shown = items.slice(0, namedLimit);
  const rest = items.length - shown.length;
  if (rest > 0) shown.push(`${rest} other${rest === 1 ? "" : "s"}`);
  if (shown.length <= 2) return shown.join(" and ");
  return `${shown.slice(0, -1).join(", ")} and ${shown[shown.length - 1]}`;
}

export function coordinateLine(
  row: MatrixRow,
  cell: MatrixCell,
  environment: string,
): string {
  const where = `${folderName(cell.folder)} · ${row.class}`;
  if (environment !== "") {
    return `${where} · read by ${environment} alone`;
  }
  const overrides = (cell.overrides ?? []).map((held) => held.environment);
  if (overrides.length === 0) {
    return `${where} · all environments read it`;
  }
  return `${where} · all environments except ${names(overrides)}`;
}

export function chipTitle(
  row: MatrixRow,
  environment: string,
  override: Override | undefined,
): string {
  if (environment === "") {
    return `the ${row.key} every environment reads unless it has its own`;
  }
  if (override?.orphaned) {
    return `${environment} no longer exists, so nothing reads the value it holds for ${row.key}`;
  }
  return `the ${row.key} ${environment} reads`;
}

export function verdictLine(
  cell: MatrixCell,
  environment: string,
  set: boolean,
  orphaned: boolean,
): string {
  if (orphaned) {
    return `${environment} no longer exists, so nothing will ever read this value. Remove it.`;
  }
  if (environment !== "") {
    return set
      ? `${environment} reads this value; every other environment reads the one set for all environments.`
      : `${environment} has no value of its own, so it reads the one set for all environments. Set one here to make it differ.`;
  }
  if (set) return "A value is set here.";
  return cell.state === "required"
    ? "No value is set, and this cell is required."
    : "No value is set. This cell overrides the root when you set one.";
}

export function doneLabel(owed: number): string {
  return owed === 0
    ? "Done — return to the terminal"
    : `Return to the terminal with ${owed} cell${owed === 1 ? "" : "s"} still to fill`;
}

export function tallyLine(owed: number): string {
  return owed === 0
    ? "every required cell is filled"
    : `${owed} cell${owed === 1 ? "" : "s"} to fill`;
}

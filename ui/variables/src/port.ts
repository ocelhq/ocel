import type { Coordinate, OtherValue, State, Version } from "./model";

export class VariablesError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

export const conflict = 409;

export interface Revealed {
  values: (Coordinate & { value: string })[];
  errors: (Coordinate & { error: string })[];
}

export interface CopyResult extends Coordinate {
  saved: boolean;
  conflict?: boolean;
  error?: string;
}

export interface VariablesPort {
  read(): Promise<State>;
  reveal(cells: readonly Coordinate[]): Promise<Revealed>;
  set(at: Coordinate, value: string, version: number): Promise<void>;
  setInEnvSource(at: Coordinate, value: string): Promise<{ awaitingApproval: boolean }>;
  remove(at: Coordinate, version: number): Promise<void>;
  history(at: Coordinate): Promise<Version[]>;
  other(): Promise<{ tier: string; values: OtherValue[] }>;
  copy(cells: readonly (Coordinate & { version: number })[]): Promise<CopyResult[]>;
}

export interface SessionPort {
  attend(): Promise<void>;
  done(): Promise<void>;
  abandon(): Promise<void>;
}

let installed: VariablesPort | null = null;
let session: SessionPort | null = null;

export function install(port: VariablesPort, attending?: SessionPort): void {
  installed = port;
  session = attending ?? null;
}

export function port(): VariablesPort {
  if (installed === null) throw new Error("@ui/variables: install(port) before reading anything");
  return installed;
}

export function sessionPort(): SessionPort | null {
  return session;
}

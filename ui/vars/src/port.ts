import type { Address, OtherValue, State, Version } from "./model";

export class VarsError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

export const conflict = 409;

export interface Revealed {
  values: (Address & { value: string })[];
  errors: (Address & { error: string })[];
}

export interface CopyResult extends Address {
  saved: boolean;
  conflict?: boolean;
  error?: string;
}

export interface VarsPort {
  read(): Promise<State>;
  reveal(cells: readonly Address[]): Promise<Revealed>;
  set(at: Address, value: string, version: number): Promise<void>;
  remove(at: Address, version: number): Promise<void>;
  history(at: Address): Promise<Version[]>;
  other(): Promise<{ tier: string; values: OtherValue[] }>;
  copy(cells: readonly (Address & { version: number })[]): Promise<CopyResult[]>;
}

export interface SessionPort {
  attend(): Promise<void>;
  done(): Promise<void>;
  abandon(): Promise<void>;
}

let installed: VarsPort | null = null;
let session: SessionPort | null = null;

export function install(port: VarsPort, attending?: SessionPort): void {
  installed = port;
  session = attending ?? null;
}

export function port(): VarsPort {
  if (installed === null) throw new Error("@ui/vars: install(port) before reading anything");
  return installed;
}

export function sessionPort(): SessionPort | null {
  return session;
}

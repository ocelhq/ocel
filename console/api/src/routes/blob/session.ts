export type FileState = "pending" | "succeeded" | "expired";

export interface SessionFile {
  key: string;
  name: string;
  size: number;
  mimeType: string;
  state: FileState;
}

export type SessionState = "pending" | "succeeded" | "expired";

export function aggregateState(files: SessionFile[]): SessionState {
  if (files.length === 0) return "pending";
  if (files.some((f) => f.state === "expired")) return "expired";
  if (files.every((f) => f.state === "succeeded")) return "succeeded";
  return "pending";
}

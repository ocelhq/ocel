// The lifecycle state of one file within an upload session. Mirrors the proto
// UploadState set; "pending" is the initial write, the detector transitions to
// "succeeded", and the expiry sweep transitions to "expired".
export type FileState = "pending" | "succeeded" | "expired";

// One file's persisted record inside a session's `files` jsonb array. key is
// the honest (tenancy-prefixed) object key.
export interface SessionFile {
  key: string;
  name: string;
  size: number;
  mimeType: string;
  state: FileState;
}

// The session-level status returned by op=poll, aggregated from the per-file
// states: any expired file makes the session terminally expired; otherwise the
// session is succeeded only once every file has succeeded; else it is pending.
export type SessionState = "pending" | "succeeded" | "expired";

export function aggregateState(files: SessionFile[]): SessionState {
  if (files.length === 0) return "pending";
  if (files.some((f) => f.state === "expired")) return "expired";
  if (files.every((f) => f.state === "succeeded")) return "succeeded";
  return "pending";
}

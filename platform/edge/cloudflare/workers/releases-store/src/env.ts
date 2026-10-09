import type { ReleasesStore } from "./releases-do";

export interface Env {
  RELEASES_DO: DurableObjectNamespace<ReleasesStore>;
  BOOTSTRAP_SECRET: string;
}

import type {
  Deployment,
  DeploymentKind,
  DeploymentOutcome,
  DeploymentTrigger,
} from "@console/db/schema";

export const PROMOTION_KINDS: readonly DeploymentKind[] = ["deploy", "preview-up", "rollback"];

export const TEARDOWN_KINDS: readonly DeploymentKind[] = ["destroy", "preview-rm"];

export function promotes(kind: DeploymentKind): boolean {
  return PROMOTION_KINDS.includes(kind);
}

export function tearsDown(kind: DeploymentKind): boolean {
  return TEARDOWN_KINDS.includes(kind);
}

export type Tone = "go" | "faint" | "destructive";

export type RunStatus = { word: string; tone: Tone };

const kindWords: Record<DeploymentKind, string> = {
  deploy: "Deployed",
  "preview-up": "Deployed",
  rollback: "Rolled back",
  destroy: "Destroyed",
  "preview-rm": "Removed",
};

export function runStatus(run: { kind: DeploymentKind; outcome: DeploymentOutcome }): RunStatus {
  if (run.outcome === "failed") {
    return { word: "Failed", tone: "destructive" };
  }
  return { word: kindWords[run.kind], tone: promotes(run.kind) ? "go" : "faint" };
}

export const kindVerbs: Record<DeploymentKind, string> = {
  deploy: "deploy",
  "preview-up": "preview",
  rollback: "rollback",
  destroy: "destroy",
  "preview-rm": "preview removal",
};

export function shortId(id: string | null): string | null {
  return id ? id.slice(0, 7) : null;
}

export function duration(run: Pick<Deployment, "startedAt" | "deployedAt">): string | null {
  if (!run.startedAt) {
    return null;
  }
  return spanOf(run.startedAt.getTime(), run.deployedAt.getTime());
}

export function spanOf(from: number, to: number): string {
  const seconds = Math.max(0, Math.round((to - from) / 1000));
  if (seconds < 60) {
    return `${seconds}s`;
  }
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) {
    return `${minutes}m ${seconds % 60}s`;
  }
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`;
}

export function runHref(slug: string, id: string): string {
  return `/projects/${slug}/deployments/${id}`;
}

export function commandOf(run: {
  kind: DeploymentKind;
  environmentIdentity: string;
  promotionId: string | null;
}): string {
  switch (run.kind) {
    case "deploy":
      return "ocel deploy";
    case "preview-up":
      return `ocel preview up ${run.environmentIdentity}`.trim();
    case "preview-rm":
      return `ocel preview rm ${run.environmentIdentity}`.trim();
    case "rollback":
      return `ocel rollback ${shortId(run.promotionId) ?? ""}`.trim();
    case "destroy":
      return "ocel destroy";
  }
}

export function initials(name: string): string {
  return name
    .split(/[\s._-]+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((part) => part[0]?.toUpperCase() ?? "")
    .join("");
}

export function authorOf(trigger: DeploymentTrigger): string | null {
  return trigger.actor ?? null;
}

import { db } from "@console/db";
import { type Deployment, deployment } from "@console/db/schema";
import { and, desc, eq, inArray, lt } from "drizzle-orm";
import type { Environment } from "@/lib/environment";
import { PROMOTION_KINDS, promotes } from "@/lib/runs";

export function environmentKey(row: Pick<Deployment, "environmentClass" | "environmentIdentity">) {
  return `${row.environmentClass}/${row.environmentIdentity}`;
}

export type ActiveRun = Pick<
  Deployment,
  | "id"
  | "projectId"
  | "kind"
  | "promotionId"
  | "tag"
  | "environmentClass"
  | "environmentIdentity"
  | "deployedAt"
>;

function runKey(row: Pick<Deployment, "projectId" | "environmentClass" | "environmentIdentity">) {
  return `${row.projectId}/${environmentKey(row)}`;
}

async function activeRuns(projectIds: string[]): Promise<Map<string, ActiveRun>> {
  if (projectIds.length === 0) {
    return new Map();
  }
  const rows = await db
    .selectDistinctOn(
      [deployment.projectId, deployment.environmentClass, deployment.environmentIdentity],
      {
        id: deployment.id,
        projectId: deployment.projectId,
        kind: deployment.kind,
        promotionId: deployment.promotionId,
        tag: deployment.tag,
        environmentClass: deployment.environmentClass,
        environmentIdentity: deployment.environmentIdentity,
        deployedAt: deployment.deployedAt,
      },
    )
    .from(deployment)
    .where(and(inArray(deployment.projectId, projectIds), eq(deployment.outcome, "succeeded")))
    .orderBy(
      deployment.projectId,
      deployment.environmentClass,
      deployment.environmentIdentity,
      desc(deployment.deployedAt),
    );

  const active = new Map<string, ActiveRun>();
  for (const row of rows) {
    if (promotes(row.kind)) {
      active.set(runKey(row), row);
    }
  }
  return active;
}

export type OverviewLoad =
  | { error: true }
  | { error: false; latest: Deployment | null; lastPromoted: Deployment | null };

export async function latestDeployments(
  projectId: string,
  environmentClass: Environment,
): Promise<OverviewLoad> {
  const scope = and(
    eq(deployment.projectId, projectId),
    eq(deployment.environmentClass, environmentClass),
  );

  try {
    const [[latest], [lastPromoted]] = await Promise.all([
      db.select().from(deployment).where(scope).orderBy(desc(deployment.deployedAt)).limit(1),
      db
        .select()
        .from(deployment)
        .where(
          and(
            scope,
            eq(deployment.outcome, "succeeded"),
            inArray(deployment.kind, [...PROMOTION_KINDS]),
          ),
        )
        .orderBy(desc(deployment.deployedAt))
        .limit(1),
    ]);

    return { error: false, latest: latest ?? null, lastPromoted: lastPromoted ?? null };
  } catch {
    return { error: true };
  }
}

export type RunRow = Omit<Deployment, "trace" | "topology"> & {
  apps: Deployment["topology"]["apps"];
  active: boolean;
};

export type RunsLoad = { error: true } | { error: false; rows: RunRow[]; more: boolean };

export const RUNS_PAGE = 50;

export async function listRuns(
  projectIds: string[],
  environmentClass: Environment | null,
  before: Date | null,
): Promise<RunsLoad> {
  if (projectIds.length === 0) {
    return { error: false, rows: [], more: false };
  }
  const filters = [inArray(deployment.projectId, projectIds)];
  if (environmentClass) {
    filters.push(eq(deployment.environmentClass, environmentClass));
  }
  if (before) {
    filters.push(lt(deployment.deployedAt, before));
  }

  try {
    const [rows, active] = await Promise.all([
      db
        .select()
        .from(deployment)
        .where(and(...filters))
        .orderBy(desc(deployment.deployedAt))
        .limit(RUNS_PAGE + 1),
      activeRuns(projectIds),
    ]);

    const page = rows.slice(0, RUNS_PAGE).map(({ trace: _trace, topology, ...row }) => ({
      ...row,
      apps: topology.apps,
      active: active.get(runKey(row))?.id === row.id,
    }));

    return { error: false, rows: page, more: rows.length > RUNS_PAGE };
  } catch {
    return { error: true };
  }
}

export type RunLoad =
  | { error: true }
  | { error: false; run: null }
  | { error: false; run: Deployment; active: ActiveRun | null };

export async function findRun(projectId: string, id: string): Promise<RunLoad> {
  try {
    const [[run], active] = await Promise.all([
      db
        .select()
        .from(deployment)
        .where(and(eq(deployment.projectId, projectId), eq(deployment.id, id))),
      activeRuns([projectId]),
    ]);
    if (!run) {
      return { error: false, run: null };
    }
    return { error: false, run, active: active.get(runKey(run)) ?? null };
  } catch {
    return { error: true };
  }
}

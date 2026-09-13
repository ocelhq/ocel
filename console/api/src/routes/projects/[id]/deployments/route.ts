import { db } from "@console/db";
import {
  type Deployment,
  deployment,
  ENVIRONMENT_CLASSES,
  type EnvironmentClass,
} from "@console/db/schema";
import { and, desc, eq } from "drizzle-orm";
import { uuidv7 } from "uuidv7";
import { readBody } from "../../../../body";
import { findOwnedProject } from "../owned";
import { deploymentRecordSchema } from "./validation";

function isEnvironmentClass(value: string | null): value is EnvironmentClass {
  return value !== null && (ENVIRONMENT_CLASSES as readonly string[]).includes(value);
}

function summary(row: Deployment) {
  return {
    id: row.id,
    runId: row.runId,
    kind: row.kind,
    promotionId: row.promotionId,
    tag: row.tag,
    environment: { class: row.environmentClass, identity: row.environmentIdentity },
    provider: { name: row.providerName, region: row.providerRegion },
    target: row.target,
    edgeKind: row.edgeKind,
    outcome: row.outcome,
    error: row.error,
    trigger: row.trigger,
    git: row.git,
    cliVersion: row.cliVersion,
    startedAt: row.startedAt?.getTime() ?? null,
    deployedAt: row.deployedAt.getTime(),
    apps: row.topology.apps.map((app) => ({
      name: app.name,
      outcome: app.outcome,
      urls: app.urls,
    })),
  };
}

export async function createDeployment(request: Request, id: string): Promise<Response> {
  const owned = await findOwnedProject(request, id);
  if (!owned.ok) {
    return owned.refusal;
  }

  const parsed = await readBody(request, deploymentRecordSchema);
  if (!parsed.ok) {
    return parsed.refusal;
  }

  const record = parsed.data;
  const values = {
    id: uuidv7(),
    projectId: owned.projectId,
    runId: record.runId,
    kind: record.kind,
    environmentClass: record.environment.class,
    environmentIdentity: record.environment.identity ?? "",
    promotionId: record.promotionId ?? null,
    tag: record.tag ?? null,
    providerName: record.provider.name,
    providerRegion: record.provider.region ?? null,
    target: record.target,
    edgeKind: record.edge?.kind ?? null,
    outcome: record.outcome,
    error: record.error ?? null,
    trigger: record.trigger,
    git: record.git ?? null,
    cliVersion: record.cliVersion ?? null,
    startedAt: record.startedAt ? new Date(record.startedAt) : null,
    deployedAt: new Date(record.deployedAt),
    trace: record.trace,
    topology: { apps: record.apps, resources: record.resources, usages: record.usages },
  };

  const [inserted] = await db.insert(deployment).values(values).onConflictDoNothing().returning();

  const row =
    inserted ??
    (
      await db
        .select()
        .from(deployment)
        .where(and(eq(deployment.projectId, owned.projectId), eq(deployment.runId, record.runId)))
    )[0];

  if (!row) {
    return Response.json(
      { error: "This run was recorded and then taken away before it could be read back" },
      { status: 409 },
    );
  }

  return Response.json(
    {
      id: row.id,
      runId: row.runId,
      kind: row.kind,
      promotionId: row.promotionId,
      environment: { class: row.environmentClass, identity: row.environmentIdentity },
      outcome: row.outcome,
      deployedAt: row.deployedAt.getTime(),
    },
    { status: inserted ? 201 : 200 },
  );
}

export async function listDeployments(request: Request, id: string): Promise<Response> {
  const owned = await findOwnedProject(request, id);
  if (!owned.ok) {
    return owned.refusal;
  }

  const env = new URL(request.url).searchParams.get("env");
  const where = isEnvironmentClass(env)
    ? and(eq(deployment.projectId, owned.projectId), eq(deployment.environmentClass, env))
    : eq(deployment.projectId, owned.projectId);

  const rows = await db
    .select()
    .from(deployment)
    .where(where)
    .orderBy(desc(deployment.deployedAt))
    .limit(50);

  return Response.json(rows.map(summary), { status: 200 });
}

export async function getDeployment(
  request: Request,
  id: string,
  deploymentId: string,
): Promise<Response> {
  const owned = await findOwnedProject(request, id);
  if (!owned.ok) {
    return owned.refusal;
  }

  const [row] = await db
    .select()
    .from(deployment)
    .where(and(eq(deployment.projectId, owned.projectId), eq(deployment.id, deploymentId)));

  if (!row) {
    return Response.json({ error: "Not found" }, { status: 404 });
  }

  return Response.json(
    {
      ...summary(row),
      trace: row.trace,
      apps: row.topology.apps,
      resources: row.topology.resources,
      usages: row.topology.usages,
    },
    { status: 200 },
  );
}

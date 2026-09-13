import { envvars, type Stored } from "@console/connectors";
import { db } from "@console/db";
import { project } from "@console/db/schema";
import { and, eq } from "drizzle-orm";
import { notFound } from "next/navigation";
import { requireOrganization } from "@/lib/access";
import { abilityFor, connectorFor, dial } from "@/lib/connectors";
import { latestTopology, namedEnvironments } from "@/lib/project-variables";
import { stateOf } from "@/lib/variables";
import { PageShell } from "../../../page-shell";
import { Provenance } from "./provenance";
import { LoadError, NeverDeployed, NoConnector, Refused } from "./states";
import { VariablesTable } from "./table";

export default async function VariablesPage({
  params,
  searchParams,
}: {
  params: Promise<{ slug: string }>;
  searchParams: Promise<{ env?: string }>;
}) {
  const { slug } = await params;
  const { env } = await searchParams;
  const held = env === "preview" ? "preview" : "production";

  const session = await requireOrganization();
  const [found] = await db
    .select({ id: project.id, slug: project.slug })
    .from(project)
    .where(and(eq(project.organizationId, session.activeOrganizationId), eq(project.slug, slug)));
  if (!found) {
    notFound();
  }

  const latest = await latestTopology(found.id, held);
  if (latest.error) {
    return (
      <PageShell title="Variables">
        <LoadError />
      </PageShell>
    );
  }
  if (latest.row === null) {
    return (
      <PageShell title="Variables">
        <NeverDeployed />
      </PageShell>
    );
  }

  const environments = await namedEnvironments(found.id);
  const connector = await connectorFor(session.activeOrganizationId, latest.row.target);
  const can = await abilityFor(session, connector);

  let stored: readonly Stored[] = [];
  let refusal = null;
  const dialled = connector === null ? null : await dial(session, connector);
  if (dialled !== null) {
    const answer = await envvars.list(dialled, held, found.slug);
    if (answer.done) {
      stored = answer.result;
    } else {
      refusal = answer.refusal;
    }
  }

  const readOnly = dialled === null || refusal !== null;
  const state = stateOf(
    found.slug,
    held,
    latest.row.topology,
    stored,
    environments,
    can,
    readOnly ? "unknown" : "live",
  );
  const now = new Date().toISOString();

  return (
    <PageShell title="Variables">
      <Provenance
        deployedAt={latest.row.deployedAt.toISOString()}
        now={now}
        promotion={latest.row.promotionId}
        provider={latest.row.providerName}
        region={latest.row.providerRegion}
        live={dialled !== null && refusal === null}
      />
      {dialled === null && <NoConnector vendor={latest.row.providerName} />}
      {refusal !== null && <Refused reason={refusal.reason} message={refusal.message} />}
      <VariablesTable projectId={found.id} environment={held} initial={state} readOnly={readOnly} />
    </PageShell>
  );
}

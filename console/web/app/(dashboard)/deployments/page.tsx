import { db } from "@console/db";
import { project } from "@console/db/schema";
import { eq } from "drizzle-orm";
import { requireOrganization } from "@/lib/access";
import type { RunProject } from "../projects/[slug]/deployments/columns";
import { RunsPage, type RunsQuery } from "../projects/[slug]/deployments/runs-page";

export default async function OrganizationDeploymentsPage({
  searchParams,
}: {
  searchParams: Promise<RunsQuery>;
}) {
  const [query, session] = await Promise.all([searchParams, requireOrganization()]);
  const rows = await db
    .select({ id: project.id, slug: project.slug, name: project.name })
    .from(project)
    .where(eq(project.organizationId, session.activeOrganizationId));

  const projects: Record<string, RunProject> = {};
  for (const row of rows) {
    projects[row.id] = { slug: row.slug, name: row.name };
  }

  return <RunsPage base="/deployments" projects={projects} query={query} />;
}

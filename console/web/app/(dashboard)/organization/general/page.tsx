import { db } from "@console/db";
import { member, project } from "@console/db/schema";
import { count, eq } from "drizzle-orm";
import { notFound } from "next/navigation";
import { requireOrganization } from "@/lib/access";
import { organizationOf } from "@/lib/organization";
import { PageShell } from "../../page-shell";
import { GeneralForm } from "./form";

export default async function OrganizationGeneralPage() {
  const session = await requireOrganization();
  const [organization, [members], [projects]] = await Promise.all([
    organizationOf(session.userId, session.activeOrganizationId),
    db
      .select({ count: count() })
      .from(member)
      .where(eq(member.organizationId, session.activeOrganizationId)),
    db
      .select({ count: count() })
      .from(project)
      .where(eq(project.organizationId, session.activeOrganizationId)),
  ]);
  if (!organization) {
    notFound();
  }

  return (
    <PageShell title="General">
      <GeneralForm
        organization={{
          id: organization.id,
          name: organization.name,
          slug: organization.slug,
          role: organization.role,
          administers: organization.administers,
        }}
        members={members.count}
        projects={projects.count}
        createdAt={organization.createdAt.toISOString()}
        now={new Date().toISOString()}
      />
    </PageShell>
  );
}

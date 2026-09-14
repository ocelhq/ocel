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
  const [held, [members], [projects]] = await Promise.all([
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
  if (!held) {
    notFound();
  }

  return (
    <PageShell title="General">
      <GeneralForm
        organization={{
          id: held.id,
          name: held.name,
          slug: held.slug,
          role: held.role,
          administers: held.administers,
        }}
        members={members.count}
        projects={projects.count}
        createdAt={held.createdAt.toISOString()}
        now={new Date().toISOString()}
      />
    </PageShell>
  );
}

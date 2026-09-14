import { db } from "@console/db";
import { project } from "@console/db/schema";
import { and, eq } from "drizzle-orm";
import { notFound } from "next/navigation";
import { requireOrganization } from "@/lib/access";
import { CommandPane } from "../../../command-pane";
import { noticeBody, PageNotice, PageShell } from "../../../page-shell";
import { projectPage } from "../../../sections";

export default async function ProjectSectionPage({
  params,
}: {
  params: Promise<{ slug: string; section: string }>;
}) {
  const { slug, section } = await params;
  const page = projectPage(section);
  if (!page) {
    notFound();
  }
  const session = await requireOrganization();
  const [found] = await db
    .select({ slug: project.slug })
    .from(project)
    .where(and(eq(project.organizationId, session.activeOrganizationId), eq(project.slug, slug)));
  if (!found) {
    notFound();
  }

  return (
    <PageShell title={page.label}>
      <PageNotice heading={page.heading}>
        <p className={noticeBody}>{page.empty}</p>
        {page.command && <CommandPane command={page.command} />}
      </PageNotice>
    </PageShell>
  );
}

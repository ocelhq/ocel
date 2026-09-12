import { db } from "@console/db";
import { project } from "@console/db/schema";
import { ArrowUpRightIcon } from "@phosphor-icons/react/dist/ssr";
import { desc, eq } from "drizzle-orm";
import Link from "next/link";
import { requireOrganization } from "@/lib/access";
import { FrameworkStack } from "../framework-stack";
import { EmptyProjects } from "./empty";
import { ProjectGrid, ProjectGridCell, ProjectsShell } from "./shell";
import { placeholderStatus, StatusLine } from "./status";

const createdFormat = new Intl.DateTimeFormat("en", {
  dateStyle: "medium",
  timeZone: "UTC",
});

export default async function OverviewPage() {
  const session = await requireOrganization();
  const projects = await db
    .select({
      id: project.id,
      name: project.name,
      slug: project.slug,
      frameworks: project.frameworks,
      createdAt: project.createdAt,
    })
    .from(project)
    .where(eq(project.organizationId, session.activeOrganizationId))
    .orderBy(desc(project.createdAt));

  return (
    <ProjectsShell>
      {projects.length === 0 ? (
        <EmptyProjects />
      ) : (
        <ProjectGrid>
          {projects.map((item, index) => (
            <ProjectGridCell
              key={item.id}
              className="relative hover:z-10 hover:border-dim focus-within:z-10"
            >
              <Link
                href={`/projects/${item.slug}`}
                className="group/card flex h-full min-h-44 flex-col justify-between gap-8 p-5 outline-none transition-colors hover:bg-muted/50 focus-visible:bg-muted/50 focus-visible:ring-2 focus-visible:ring-ring/40 focus-visible:ring-inset"
              >
                <div className="flex items-start justify-between gap-4">
                  <FrameworkStack frameworks={item.frameworks} />
                  <ArrowUpRightIcon
                    aria-hidden
                    className="size-4 text-dim transition-[color,translate] group-hover/card:translate-x-0.5 group-hover/card:-translate-y-0.5 group-hover/card:text-foreground"
                  />
                </div>
                <div className="flex min-w-0 flex-col gap-2">
                  <h2 className="truncate text-lg/6 font-semibold tracking-tight">{item.name}</h2>
                  <StatusLine status={placeholderStatus(index)} />
                  <p className="flex min-w-0 items-center gap-2 text-xs text-muted-foreground">
                    <span className="truncate font-mono">{item.slug}</span>
                    <span aria-hidden className="text-faint">
                      /
                    </span>
                    <span className="shrink-0">
                      Created{" "}
                      <time dateTime={item.createdAt.toISOString()}>
                        {createdFormat.format(item.createdAt)}
                      </time>
                    </span>
                  </p>
                </div>
              </Link>
            </ProjectGridCell>
          ))}
        </ProjectGrid>
      )}
    </ProjectsShell>
  );
}

import { db } from "@console/db";
import { project } from "@console/db/schema";
import { ArrowRightIcon } from "@phosphor-icons/react/dist/ssr";
import { asc, eq } from "drizzle-orm";
import Link from "next/link";
import { requireOrganization } from "@/lib/access";
import type { Environment } from "@/lib/environment";
import { EmptyProjects } from "./(overview)/empty";
import { projectHref } from "./sections";

export async function ProjectPicker({
  section,
  environment,
}: {
  section: string;
  environment: Environment;
}) {
  const session = await requireOrganization();
  const projects = await db
    .select({ name: project.name, slug: project.slug })
    .from(project)
    .where(eq(project.organizationId, session.activeOrganizationId))
    .orderBy(asc(project.name));

  if (projects.length === 0) {
    return <EmptyProjects />;
  }

  return (
    <ul className="flex max-w-2xl flex-col divide-y divide-border border border-border">
      {projects.map((item) => (
        <li key={item.slug}>
          <Link
            href={projectHref(item.slug, section, environment)}
            className="group/pick flex items-center justify-between gap-4 px-5 py-4 outline-none transition-colors hover:bg-muted/50 focus-visible:bg-muted/50 focus-visible:ring-2 focus-visible:ring-ring/40 focus-visible:ring-inset"
          >
            <span className="flex min-w-0 flex-col gap-0.5">
              <span className="truncate font-medium">{item.name}</span>
              <span className="truncate font-mono text-xs text-muted-foreground">{item.slug}</span>
            </span>
            <ArrowRightIcon
              aria-hidden
              className="size-4 shrink-0 text-dim transition-[color,translate] group-hover/pick:translate-x-0.5 group-hover/pick:text-foreground"
            />
          </Link>
        </li>
      ))}
    </ul>
  );
}

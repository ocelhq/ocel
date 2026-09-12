import { db } from "@console/db";
import { project } from "@console/db/schema";
import { asc, eq } from "drizzle-orm";
import { cookies } from "next/headers";
import { redirect } from "next/navigation";
import type { ReactNode } from "react";
import { SidebarInset, SidebarProvider } from "@/components/ui/sidebar";
import { getViewer, listMemberships, requireOrganization } from "@/lib/access";
import { SIDEBAR_STATE_COOKIE, SIDEBAR_WIDTH_COOKIE } from "@/lib/sidebar-layout";
import { HeaderSidebarTrigger } from "./header-sidebar-trigger";
import { type ProjectOption, ProjectSwitcher } from "./project-switcher";
import { AppSidebar } from "./sidebar";

export default async function DashboardLayout({ children }: { children: ReactNode }) {
  const session = await requireOrganization();
  const [viewer, organizations, projects, store] = await Promise.all([
    getViewer(session.userId),
    listMemberships(session.userId),
    db
      .select({ name: project.name, slug: project.slug })
      .from(project)
      .where(eq(project.organizationId, session.activeOrganizationId))
      .orderBy(asc(project.name))
      .catch((): ProjectOption[] | null => null),
    cookies(),
  ]);

  if (!viewer) {
    redirect("/sign-in");
  }

  return (
    <SidebarProvider
      defaultOpen={store.get(SIDEBAR_STATE_COOKIE)?.value !== "false"}
      defaultWidth={Number(store.get(SIDEBAR_WIDTH_COOKIE)?.value) || undefined}
    >
      <AppSidebar
        viewer={viewer}
        organizations={organizations}
        activeOrganizationId={session.activeOrganizationId}
      />
      <SidebarInset>
        <header className="flex h-14 shrink-0 items-center gap-3 border-b border-border px-5 md:px-8">
          <HeaderSidebarTrigger />
          <ProjectSwitcher projects={projects} />
        </header>
        {children}
      </SidebarInset>
    </SidebarProvider>
  );
}

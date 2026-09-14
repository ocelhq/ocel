import Link from "next/link";
import { Button } from "@/components/ui/button";
import { listRuns } from "@/lib/deployments";
import { runScopeOf } from "@/lib/environment";
import { PageShell } from "../../../page-shell";
import type { RunProject } from "./columns";
import { RunFilter } from "./filter";
import { Frame, LoadError, NeverDeployed, NothingOlder } from "./states";
import { RunRows } from "./table";

export type RunsQuery = { [key: string]: string | string[] | undefined };

export async function RunsPage({
  base,
  projects,
  query,
}: {
  base: string;
  projects: Record<string, RunProject>;
  query: RunsQuery;
}) {
  const scope = runScopeOf(typeof query.env === "string" ? query.env : null);
  const beforeMs = typeof query.before === "string" ? Number(query.before) : Number.NaN;
  const before = Number.isFinite(beforeMs) ? new Date(beforeMs) : null;
  const withProject = base === "/deployments";

  const href = (older?: number) => {
    const params = new URLSearchParams();
    if (scope !== "all") {
      params.set("env", scope);
    }
    if (older) {
      params.set("before", String(older));
    }
    const search = params.toString();
    return `${base}${search ? `?${search}` : ""}`;
  };

  const load = await listRuns(Object.keys(projects), scope === "all" ? null : scope, before);
  const now = new Date().toISOString();

  return (
    <PageShell title="Deployments">
      <div className="flex flex-col gap-3">
        <RunFilter />
        <Frame withProject={withProject}>
          {load.error ? (
            <LoadError href={href(before?.getTime())} withProject={withProject} />
          ) : load.rows.length === 0 ? (
            before ? (
              <NothingOlder href={href()} withProject={withProject} />
            ) : (
              <NeverDeployed scope={scope} withProject={withProject} />
            )
          ) : (
            <RunRows rows={load.rows} projects={projects} withProject={withProject} now={now} />
          )}
        </Frame>
        {!load.error && (before || load.more) && (
          <div className="flex items-center gap-2">
            {before && (
              <Button
                variant="outline"
                size="sm"
                nativeButton={false}
                render={<Link href={href()} />}
              >
                Newest
              </Button>
            )}
            {load.more && (
              <Button
                variant="outline"
                size="sm"
                nativeButton={false}
                render={<Link href={href(load.rows[load.rows.length - 1].deployedAt.getTime())} />}
              >
                Older
              </Button>
            )}
          </div>
        )}
      </div>
    </PageShell>
  );
}

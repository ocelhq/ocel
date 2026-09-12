"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { TableBody, TableCell, TableRow } from "@/components/ui/table";
import type { RunRow } from "@/lib/deployments";
import { authorOf, commandOf, duration, runHref, runStatus, shortId } from "@/lib/runs";
import { Stamp } from "../../../stamp";
import { AppMarks, AuthorMark, EnvironmentBadge, StatusDot, Trigger } from "./cells";
import type { RunProject } from "./columns";

const cell = "h-14 px-5 first:pl-5 last:pr-5";

function Row({
  run,
  project,
  withProject,
  now,
}: {
  run: RunRow;
  project: RunProject;
  withProject: boolean;
  now: string;
}) {
  const router = useRouter();
  const href = runHref(project.slug, run.id);
  const status = runStatus(run);
  const took = duration(run);
  const author = authorOf(run.trigger);

  return (
    <TableRow
      onClick={(event) => {
        if (event.defaultPrevented || (event.target as HTMLElement).closest("a, button")) {
          return;
        }
        router.push(href);
      }}
      className="group cursor-pointer has-[a:focus-visible]:bg-muted/50"
    >
      {withProject && (
        <TableCell className={`${cell} font-medium`}>
          <Link
            href={`/projects/${project.slug}/deployments`}
            className="underline-offset-4 outline-none hover:underline focus-visible:underline"
          >
            {project.name}
          </Link>
        </TableCell>
      )}
      <TableCell className={cell}>
        <span className="inline-flex items-baseline gap-2">
          <Link
            href={href}
            className="font-medium text-foreground underline-offset-4 outline-none group-hover:underline focus-visible:underline"
          >
            {shortId(run.promotionId) ?? "—"}
          </Link>
          {run.tag && <span className="text-muted-foreground">{run.tag}</span>}
        </span>
      </TableCell>
      <TableCell className={cell}>
        <span className="inline-flex items-baseline gap-2">
          <StatusDot tone={status.tone} />
          <span className={status.tone === "destructive" ? "text-destructive" : ""}>
            {status.word}
          </span>
          {took && (
            <span className="text-muted-foreground">
              in <span className="tabular-nums">{took}</span>
            </span>
          )}
        </span>
      </TableCell>
      <TableCell className={cell}>
        <EnvironmentBadge environmentClass={run.environmentClass} active={run.active} />
      </TableCell>
      <TableCell className={cell}>
        <AppMarks apps={run.apps} />
      </TableCell>
      <TableCell className={cell}>
        <Trigger trigger={run.trigger} command={commandOf(run)} />
      </TableCell>
      <TableCell className={`${cell} text-right text-muted-foreground`}>
        <span className="inline-flex items-center gap-2">
          <Stamp at={run.deployedAt.toISOString()} now={now} />
          {author && (
            <AuthorMark
              name={author}
              trigger={run.trigger}
              deployedAt={run.deployedAt.toISOString()}
            />
          )}
        </span>
      </TableCell>
    </TableRow>
  );
}

export function RunRows({
  rows,
  projects,
  withProject,
  now,
}: {
  rows: RunRow[];
  projects: Record<string, RunProject>;
  withProject: boolean;
  now: string;
}) {
  return (
    <TableBody>
      {rows.map((run) => (
        <Row
          key={run.id}
          run={run}
          project={projects[run.projectId]}
          withProject={withProject}
          now={now}
        />
      ))}
    </TableBody>
  );
}

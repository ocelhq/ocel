"use client";

import { ArrowClockwiseIcon } from "@phosphor-icons/react";
import { useEffect } from "react";
import { Button } from "@/components/ui/button";
import { ProjectGrid, ProjectGridCell, ProjectsShell } from "./shell";

export default function ProjectsError({
  error,
  unstable_retry,
}: {
  error: Error & { digest?: string };
  unstable_retry: () => void;
}) {
  useEffect(() => {
    console.error(error);
  }, [error]);

  return (
    <ProjectsShell>
      <ProjectGrid className="md:grid-cols-1 xl:grid-cols-1">
        <ProjectGridCell className="flex flex-col items-start gap-5 px-5 py-10 md:px-10">
          <div className="flex max-w-prose flex-col gap-1" role="alert">
            <h2 className="text-base font-semibold tracking-tight">Projects didn&rsquo;t load</h2>
            <p className="text-muted-foreground">
              The console couldn&rsquo;t reach its database. Nothing in your cloud account is
              affected. Try again, and if it keeps failing, check that the console&rsquo;s Postgres
              is running.
            </p>
            {error.digest && (
              <p className="mt-2 font-mono text-xs text-muted-foreground">
                Reference {error.digest}
              </p>
            )}
          </div>
          <Button variant="outline" onClick={() => unstable_retry()}>
            <ArrowClockwiseIcon />
            Try again
          </Button>
        </ProjectGridCell>
      </ProjectGrid>
    </ProjectsShell>
  );
}

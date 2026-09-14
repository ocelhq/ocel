"use client";

import { ArrowClockwiseIcon } from "@phosphor-icons/react";
import { useEffect } from "react";
import { Button } from "@/components/ui/button";
import { noticeBody, PageNotice } from "../page-shell";
import { ProjectsShell } from "./shell";

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
      <PageNotice heading="Projects didn’t load" role="alert">
        <p className={noticeBody}>
          The console couldn&rsquo;t reach its database. Nothing in your cloud account is affected.
          Try again, and if it keeps failing, check that the console&rsquo;s Postgres is running.
        </p>
        {error.digest && (
          <p className="font-mono text-xs text-muted-foreground">Reference {error.digest}</p>
        )}
        <Button variant="outline" onClick={() => unstable_retry()}>
          <ArrowClockwiseIcon data-icon="inline-start" />
          Try again
        </Button>
      </PageNotice>
    </ProjectsShell>
  );
}

import type { LatestRun } from "@/lib/deployments";
import { runStatus, tearsDown } from "@/lib/runs";
import { cn } from "@/lib/utils";
import { Stamp } from "../stamp";

export function StatusLine({ run, now }: { run: LatestRun | undefined; now: string }) {
  if (!run) {
    return (
      <span className="flex items-center gap-2 text-muted-foreground">
        <span aria-hidden className="size-1.5 shrink-0 border border-dim" />
        Not deployed yet
      </span>
    );
  }
  const status = runStatus(run);
  const word =
    run.outcome === "failed"
      ? tearsDown(run.kind)
        ? "Teardown failed"
        : "Deploy failed"
      : status.word;
  return (
    <span
      className={cn(
        "flex items-center gap-2",
        status.tone === "destructive" ? "text-destructive" : "text-foreground/80",
      )}
    >
      <span
        aria-hidden
        className={cn(
          "size-1.5 shrink-0",
          status.tone === "go" && "bg-go",
          status.tone === "faint" && "bg-dim",
          status.tone === "destructive" && "bg-destructive",
        )}
      />
      <Stamp at={run.deployedAt.toISOString()} now={now} prefix={word} />
    </span>
  );
}

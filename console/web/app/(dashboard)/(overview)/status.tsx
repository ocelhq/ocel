import { cn } from "@/lib/utils";

type Status = { label: string; tone: "idle" | "live" | "gone" };

// TODO: derive from deployment records once the console stores them.
const placeholderStatuses: Status[] = [
  { label: "Not deployed yet", tone: "idle" },
  { label: "Last deploy 2 minutes ago", tone: "live" },
  { label: "Torn down 1 week ago", tone: "gone" },
];

export function placeholderStatus(index: number): Status {
  return placeholderStatuses[index % placeholderStatuses.length];
}

export function StatusLine({ status }: { status: Status }) {
  return (
    <span className="flex items-center gap-2 text-foreground/80">
      <span
        aria-hidden
        className={cn(
          "size-1.5 shrink-0",
          status.tone === "live" && "bg-go",
          status.tone === "idle" && "border border-dim",
          status.tone === "gone" && "bg-dim",
        )}
      />
      {status.label}
    </span>
  );
}

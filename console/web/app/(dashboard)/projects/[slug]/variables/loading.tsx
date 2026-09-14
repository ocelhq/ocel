import { Skeleton } from "@/components/ui/skeleton";
import { PageShell } from "../../../page-shell";

export default function VariablesLoading() {
  return (
    <PageShell title="Variables">
      <Skeleton className="h-3 w-96" />
      <div className="border border-border">
        <div className="flex items-center gap-3 border-b border-border px-4 py-3">
          <Skeleton className="h-8 w-28" />
          <Skeleton className="ml-auto h-8 w-56" />
        </div>
        {Array.from({ length: 7 }, (_, row) => (
          <div key={row} className="flex h-14 items-center gap-4 border-b border-border px-4">
            <Skeleton className="size-4 shrink-0" />
            <Skeleton className="h-3.5 w-48" />
            <Skeleton className="ml-auto h-8 w-[40%]" />
          </div>
        ))}
      </div>
    </PageShell>
  );
}

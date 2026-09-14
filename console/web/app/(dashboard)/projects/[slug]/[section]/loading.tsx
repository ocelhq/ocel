import { Skeleton } from "@/components/ui/skeleton";
import { PageShell } from "../../../page-shell";

export default function ProjectSectionLoading() {
  return (
    <PageShell title={<Skeleton className="h-7 w-40" />}>
      <div className="flex max-w-2xl flex-col gap-5 border border-border px-5 py-8 md:px-8">
        <Skeleton className="h-5 w-56" />
        <Skeleton className="h-3.5 w-full" />
        <Skeleton className="h-3.5 w-2/3" />
        <Skeleton className="h-20 w-full max-w-md" />
      </div>
    </PageShell>
  );
}

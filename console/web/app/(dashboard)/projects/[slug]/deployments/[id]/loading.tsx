import { Skeleton } from "@/components/ui/skeleton";

export default function RunLoading() {
  return (
    <div className="flex flex-1 flex-col gap-8 px-5 pt-6 pb-12 md:px-8">
      <div className="flex flex-col gap-4">
        <Skeleton className="h-3 w-24" />
        <div className="flex items-start justify-between gap-6">
          <div className="flex flex-col gap-3">
            <Skeleton className="h-7 w-32" />
            <Skeleton className="h-4 w-72" />
          </div>
          <div className="flex gap-2">
            <Skeleton className="h-8 w-20" />
            <Skeleton className="h-8 w-28" />
          </div>
        </div>
      </div>
      <div className="grid gap-x-10 gap-y-5 border-t border-border pt-5 sm:grid-cols-2 lg:grid-cols-3">
        {Array.from({ length: 9 }, (_, at) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: static placeholder rows
          <div key={at} className="flex flex-col gap-1.5">
            <Skeleton className="h-3 w-16" />
            <Skeleton className="h-4 w-40" />
          </div>
        ))}
      </div>
      <Skeleton className="h-32 w-full" />
      <Skeleton className="h-48 w-full" />
    </div>
  );
}

import { Skeleton } from "@/components/ui/skeleton";
import { ProjectGrid, ProjectGridCell, ProjectsShell } from "./shell";

export default function ProjectsLoading() {
  return (
    <ProjectsShell>
      <ProjectGrid>
        {Array.from({ length: 6 }, (_, index) => `cell-${index}`).map((key) => (
          <ProjectGridCell key={key} className="flex min-h-44 flex-col justify-between gap-8 p-5">
            <Skeleton className="size-8" />
            <div className="flex flex-col gap-1.5">
              <Skeleton className="h-6 w-2/5" />
              <Skeleton className="h-5 w-1/2" />
              <div className="mt-0.5 flex items-center justify-between">
                <Skeleton className="h-4 w-2/5" />
                <Skeleton className="h-4 w-1/5" />
              </div>
            </div>
          </ProjectGridCell>
        ))}
      </ProjectGrid>
    </ProjectsShell>
  );
}

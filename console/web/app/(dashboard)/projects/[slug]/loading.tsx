import { MapSkeleton } from "./overview/states";

export default function ProjectOverviewLoading() {
  return (
    <div className="h-[calc(100svh-3.5rem)] min-h-0 overflow-hidden">
      <MapSkeleton />
    </div>
  );
}

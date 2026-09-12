import { CommandPane } from "../command-pane";
import { ProjectGrid, ProjectGridCell } from "./shell";

export function EmptyProjects() {
  return (
    <ProjectGrid className="md:grid-cols-1 xl:grid-cols-1">
      <ProjectGridCell className="flex flex-col gap-5 px-5 py-10 md:px-10">
        <div className="flex max-w-prose flex-col gap-1">
          <h2 className="text-base font-semibold tracking-tight">No projects yet</h2>
          <p className="text-muted-foreground">
            Projects start from code. Run this in your app&rsquo;s directory to create one, or to
            link the app to a project that already exists.
          </p>
        </div>
        <CommandPane command="ocel link" />
      </ProjectGridCell>
    </ProjectGrid>
  );
}

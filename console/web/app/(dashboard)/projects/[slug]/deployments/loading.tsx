import { Skeleton } from "@/components/ui/skeleton";
import { TableBody, TableCell, TableRow } from "@/components/ui/table";
import { PageShell } from "../../../page-shell";
import { runColumns } from "./columns";
import { Frame } from "./states";

export function RunsSkeleton({ withProject }: { withProject: boolean }) {
  const columns = runColumns(withProject);
  return (
    <PageShell title="Deployments">
      <div className="flex flex-col gap-3">
        <Skeleton className="h-8 w-40" />
        <Frame withProject={withProject}>
          <TableBody>
            {Array.from({ length: 8 }, (_, row) => (
              <TableRow key={row} className="hover:bg-transparent">
                {columns.map((column) => (
                  <TableCell key={column.key} className="h-14 px-5 first:pl-5 last:pr-5">
                    <Skeleton
                      className={`h-3.5 ${column.className.includes("text-right") ? "ml-auto w-12" : "w-3/4"}`}
                    />
                  </TableCell>
                ))}
              </TableRow>
            ))}
          </TableBody>
        </Frame>
      </div>
    </PageShell>
  );
}

export default function DeploymentsLoading() {
  return <RunsSkeleton withProject={false} />;
}

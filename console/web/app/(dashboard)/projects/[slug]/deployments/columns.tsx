import { TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { labelType } from "@/lib/type";

export type RunProject = { slug: string; name: string };

export function runColumns(withProject: boolean) {
  return [
    ...(withProject ? [{ key: "project", label: "Project", className: "w-44" }] : []),
    { key: "promotion", label: "Promotion", className: "w-40" },
    { key: "status", label: "Status", className: "w-40" },
    { key: "environment", label: "Environment", className: "w-40" },
    { key: "apps", label: "Apps", className: "w-28" },
    { key: "trigger", label: "Trigger", className: "" },
    { key: "deployed", label: "Deployed", className: "w-36 text-right" },
  ];
}

export function RunsHead({ withProject }: { withProject: boolean }) {
  return (
    <TableHeader>
      <TableRow className="hover:bg-transparent">
        {runColumns(withProject).map((column) => (
          <TableHead
            key={column.key}
            className={`h-9 px-5 first:pl-5 last:pr-5 ${labelType} ${column.className}`}
          >
            {column.label}
          </TableHead>
        ))}
      </TableRow>
    </TableHeader>
  );
}

import Link from "next/link";
import type { ReactNode } from "react";
import { Table, TableBody, TableCell, TableRow } from "@/components/ui/table";
import type { RunScope } from "@/lib/environment";
import { CommandPane } from "../../../command-pane";
import { RunsHead, runColumns } from "./columns";

export function Frame({ withProject, children }: { withProject: boolean; children: ReactNode }) {
  return (
    <div className="border border-border">
      <Table className="min-w-[64rem] text-sm/5">
        <RunsHead withProject={withProject} />
        {children}
      </Table>
    </div>
  );
}

const action =
  "border border-border px-3 py-2 text-sm font-medium outline-none transition-colors hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/40";

function Notice({
  withProject,
  heading,
  children,
  role,
}: {
  withProject: boolean;
  heading: string;
  children: ReactNode;
  role?: "alert";
}) {
  return (
    <TableBody>
      <TableRow className="hover:bg-transparent">
        <TableCell colSpan={runColumns(withProject).length} className="p-0 whitespace-normal">
          <div className="flex max-w-lg flex-col items-start gap-4 px-5 py-10 md:px-8" role={role}>
            <h2 className="text-base font-semibold">{heading}</h2>
            {children}
          </div>
        </TableCell>
      </TableRow>
    </TableBody>
  );
}

export function NeverDeployed({ scope, withProject }: { scope: RunScope; withProject: boolean }) {
  const command = scope === "preview" ? "ocel preview up" : "ocel deploy";
  const where = scope === "all" ? "" : ` to ${scope}`;
  return (
    <Notice withProject={withProject} heading={`Nothing deployed${where} yet`}>
      <p className="text-sm text-muted-foreground">
        Run this in a project directory. Each run reports here when it finishes, and the history
        stays even after your cloud prunes old promotions.
      </p>
      <CommandPane command={command} />
    </Notice>
  );
}

export function NothingOlder({ href, withProject }: { href: string; withProject: boolean }) {
  return (
    <Notice withProject={withProject} heading="No older runs">
      <p className="text-sm text-muted-foreground">
        The console only knows about runs since it was first told of one.
      </p>
      <Link href={href} className={action}>
        Back to newest
      </Link>
    </Notice>
  );
}

export function LoadError({ href, withProject }: { href: string; withProject: boolean }) {
  return (
    <Notice withProject={withProject} heading="Deployments didn’t load" role="alert">
      <p className="text-sm text-muted-foreground">
        Nothing in your cloud is affected. Try again in a moment.
      </p>
      <a href={href} className={action}>
        Try again
      </a>
    </Notice>
  );
}

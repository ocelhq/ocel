import { type Liveness, liveness } from "@console/api";
import { capabilities } from "@console/connectors";
import { requireOrganization } from "@/lib/access";
import { connectorsOf, dial } from "@/lib/connectors";
import { labelType } from "@/lib/type";
import { CommandPane } from "../../command-pane";
import { ProviderMark } from "../../marks";
import { noticeBody, PageNotice, PageShell } from "../../page-shell";
import { Stamp } from "../../stamp";

const tones: Record<Liveness, string> = {
  online: "bg-go",
  offline: "bg-destructive",
  never: "bg-faint",
};

const says: Record<Liveness, string> = {
  online: "Online",
  offline: "Not answering",
  never: "Never connected",
};

export default async function OrganizationConnectorsPage() {
  const session = await requireOrganization();
  const rows = await connectorsOf(session.activeOrganizationId);
  const now = new Date().toISOString();

  if (rows.length === 0) {
    return (
      <PageShell title="Connectors">
        <PageNotice heading="Nothing connected yet">
          <p className={noticeBody}>
            A connector is a small server you run in your own account. The console asks it for the
            things it must never store itself — variable values today, logs and spend later. Ocel
            never deploys or provisions from the browser.
          </p>
          <CommandPane command="ocel connector add" />
        </PageNotice>
      </PageShell>
    );
  }

  const seen = await Promise.all(
    rows.map(async (row) => {
      if (row.url === null || row.publicKey !== null) {
        return null;
      }
      const dialled = await dial(session, row);
      return dialled === null ? null : await capabilities(dialled.url, dialled.token);
    }),
  );

  return (
    <PageShell title="Connectors">
      <div className="flex flex-col border border-border">
        {rows.map((row, at) => {
          const reached = seen[at];
          const live: Liveness = reached?.done ? "online" : liveness(row);
          return (
            <div
              key={row.id}
              className="flex flex-wrap items-start gap-x-6 gap-y-3 border-b border-border px-5 py-4 last:border-b-0"
            >
              <span className="mt-1 shrink-0">
                <ProviderMark provider={row.vendor} size={18} />
              </span>
              <div className="flex min-w-56 flex-1 flex-col gap-1">
                <p className="font-medium">{row.target}</p>
                <p className="font-mono text-xs break-all text-muted-foreground">
                  {row.url ?? "no address published"}
                </p>
                <p className={labelType}>
                  {row.compute ?? "compute unset"} · {row.reach}
                </p>
              </div>
              <div className="flex min-w-40 flex-col gap-1">
                <p className="flex items-center gap-2 text-sm">
                  <span aria-hidden className={`size-2 shrink-0 ${tones[live]}`} />
                  {says[live]}
                </p>
                <p className={labelType}>
                  {row.lastSeenAt ? (
                    <>
                      seen <Stamp at={row.lastSeenAt.toISOString()} now={now} />
                    </>
                  ) : (
                    "no contact"
                  )}
                </p>
              </div>
              <ul className="flex min-w-52 flex-wrap gap-1">
                {(reached?.done ? reached.result : row.capabilities).map((held) => (
                  <li key={held} className={`border border-border px-1.5 py-0.5 ${labelType}`}>
                    {held}
                  </li>
                ))}
              </ul>
            </div>
          );
        })}
      </div>
      <PageNotice heading="Adding another">
        <p className={noticeBody}>
          One connector per bootstrapped target. It runs under that target’s own credentials, and
          the console can ask it only for the day-2 things — never a deploy.
        </p>
        <CommandPane command="ocel connector add" />
      </PageNotice>
    </PageShell>
  );
}

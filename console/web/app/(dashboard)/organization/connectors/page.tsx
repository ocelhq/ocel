import { capabilities } from "@console/connectors";
import { requireOrganization } from "@/lib/access";
import { connectorsOf, type Liveness, liveness } from "@/lib/connectors";
import { CommandPane } from "../../command-pane";
import { ProviderMark } from "../../marks";
import { PageNotice, PageShell } from "../../page-shell";
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
        <PageNotice>
          <h2 className="text-base font-semibold">Nothing connected yet</h2>
          <p className="max-w-prose text-sm text-muted-foreground">
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
    rows.map((row) => (row.url === null || row.publicKey !== null ? null : capabilities(row.url))),
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
                <p className="font-mono text-[13px]">{row.target}</p>
                <p className="font-mono text-xs break-all text-muted-foreground">
                  {row.url ?? "no address published"}
                </p>
                <p className="font-mono text-[11px] tracking-[0.14em] text-muted-foreground uppercase">
                  {row.compute ?? "compute unset"} · {row.reach}
                </p>
              </div>
              <div className="flex min-w-40 flex-col gap-1">
                <p className="flex items-center gap-2 text-sm">
                  <span aria-hidden className={`size-2 shrink-0 ${tones[live]}`} />
                  {says[live]}
                </p>
                <p className="font-mono text-[11px] tracking-[0.14em] text-muted-foreground uppercase">
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
                  <li
                    key={held}
                    className="border border-border px-1.5 py-0.5 font-mono text-[11px] tracking-[0.14em] text-muted-foreground uppercase"
                  >
                    {held}
                  </li>
                ))}
              </ul>
            </div>
          );
        })}
      </div>
      <PageNotice>
        <h2 className="text-base font-semibold">Adding another</h2>
        <p className="max-w-prose text-sm text-muted-foreground">
          One connector per bootstrapped target. It runs under that target’s own credentials, and
          the console can ask it only for the day-2 things — never a deploy.
        </p>
        <CommandPane command="ocel connector add" />
      </PageNotice>
    </PageShell>
  );
}

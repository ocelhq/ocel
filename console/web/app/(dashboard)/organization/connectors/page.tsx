import { PageNotice, PageShell } from "../../page-shell";

export default function OrganizationConnectorsPage() {
  return (
    <PageShell title="Connectors">
      <PageNotice>
        <p className="max-w-prose text-muted-foreground">No connectors are available yet.</p>
      </PageNotice>
    </PageShell>
  );
}

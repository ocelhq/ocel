import { listMemberships, requireOrganization } from "@/lib/access";
import { PageShell } from "../../page-shell";

export default async function OrganizationGeneralPage() {
  const session = await requireOrganization();
  const organizations = await listMemberships(session.userId);
  const active = organizations.find((item) => item.id === session.activeOrganizationId);

  return (
    <PageShell title="General">
      {active && (
        <dl className="grid max-w-2xl grid-cols-[auto_1fr] border border-border">
          <dt className="border-b border-border px-5 py-4 text-muted-foreground">Name</dt>
          <dd className="border-b border-border px-5 py-4 font-medium">{active.name}</dd>
          <dt className="px-5 py-4 text-muted-foreground">Slug</dt>
          <dd className="px-5 py-4 font-mono text-[13px]">{active.slug}</dd>
        </dl>
      )}
    </PageShell>
  );
}

import { PageNotice, PageShell } from "../../page-shell";

export default function OrganizationMembersPage() {
  return (
    <PageShell title="Members">
      <PageNotice>
        <p className="max-w-prose text-muted-foreground">
          Inviting and removing members isn&rsquo;t in the console yet.
        </p>
      </PageNotice>
    </PageShell>
  );
}

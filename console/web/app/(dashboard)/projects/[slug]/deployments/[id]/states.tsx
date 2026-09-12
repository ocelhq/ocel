import Link from "next/link";
import { PageNotice, PageShell } from "../../../../page-shell";

const action =
  "border border-border px-3 py-2 text-sm font-medium outline-none transition-colors hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/40";

export function NotFound({ back }: { back: string }) {
  return (
    <PageShell title="No run with this id">
      <PageNotice>
        <p className="max-w-prose text-sm text-muted-foreground">
          It may belong to another project, or the link is stale.
        </p>
        <Link href={back} className={action}>
          All deployments
        </Link>
      </PageNotice>
    </PageShell>
  );
}

export function LoadError({ href, back }: { href: string; back: string }) {
  return (
    <PageShell title="Run didn’t load">
      <PageNotice>
        <p className="max-w-prose text-sm text-muted-foreground" role="alert">
          Nothing in your cloud is affected. Try again in a moment.
        </p>
        <div className="flex gap-2">
          <a href={href} className={action}>
            Try again
          </a>
          <Link href={back} className={action}>
            All deployments
          </Link>
        </div>
      </PageNotice>
    </PageShell>
  );
}

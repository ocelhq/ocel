import Link from "next/link";
import { Button } from "@/components/ui/button";
import { noticeBody, PageNotice, PageShell } from "../../../../page-shell";

export function NotFound({ back }: { back: string }) {
  return (
    <PageShell title="Deployments">
      <PageNotice heading="No run with this id">
        <p className={noticeBody}>It may belong to another project, or the link is stale.</p>
        <Button variant="outline" nativeButton={false} render={<Link href={back} />}>
          All deployments
        </Button>
      </PageNotice>
    </PageShell>
  );
}

export function LoadError({ href, back }: { href: string; back: string }) {
  return (
    <PageShell title="Deployments">
      <PageNotice heading="Run didn’t load" role="alert">
        <p className={noticeBody}>Nothing in your cloud is affected. Try again in a moment.</p>
        <div className="flex gap-2">
          <Button variant="outline" nativeButton={false} render={<a href={href} />}>
            Try again
          </Button>
          <Button variant="outline" nativeButton={false} render={<Link href={back} />}>
            All deployments
          </Button>
        </div>
      </PageNotice>
    </PageShell>
  );
}

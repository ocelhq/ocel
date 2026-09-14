import { notFound } from "next/navigation";
import { environmentOf } from "@/lib/environment";
import { noticeBody, PageNotice, PageShell } from "../page-shell";
import { ProjectPicker } from "../project-picker";
import { projectPage } from "../sections";

export default async function PickProjectPage({
  params,
  searchParams,
}: {
  params: Promise<{ section: string }>;
  searchParams: Promise<Record<string, string | string[] | undefined>>;
}) {
  const { section } = await params;
  const page = projectPage(section);
  if (!page) {
    notFound();
  }
  if (page.aggregate) {
    return (
      <PageShell title={page.label}>
        <PageNotice heading={page.heading}>
          <p className={noticeBody}>{page.aggregate}</p>
        </PageNotice>
      </PageShell>
    );
  }
  const { env } = await searchParams;

  return (
    <PageShell
      title={page.label}
      description={`Pick a project to open its ${page.label.toLowerCase()}.`}
    >
      <ProjectPicker
        section={page.section}
        environment={environmentOf(typeof env === "string" ? env : undefined)}
      />
    </PageShell>
  );
}

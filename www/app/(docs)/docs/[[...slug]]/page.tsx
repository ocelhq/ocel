import {
  DocsBody,
  DocsDescription,
  DocsPage,
  DocsTitle,
  MarkdownCopyButton,
  ViewOptionsPopover,
} from "fumadocs-ui/layouts/docs/page";
import { createRelativeLink } from "fumadocs-ui/mdx";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { CompareWith } from "@/components/docs/compare";
import { getMDXComponents } from "@/components/docs/mdx";
import { source } from "@/lib/source";

export default async function Page(props: PageProps<"/docs/[[...slug]]">) {
  const params = await props.params;
  const page = source.getPage(params.slug);
  if (!page) notFound();

  const MDX = page.data.body;
  const components = getMDXComponents({ a: createRelativeLink(source, page) });

  if (page.url === "/docs") {
    return (
      <DocsPage
        full
        className="max-w-none items-center"
        tableOfContent={{ enabled: false }}
        tableOfContentPopover={{ enabled: false }}
        breadcrumb={{ enabled: false }}
        footer={{ enabled: false }}
      >
        <DocsBody className="landing w-full max-w-4xl pb-24">
          <MDX components={components} />
        </DocsBody>
      </DocsPage>
    );
  }

  if (page.url === "/docs/compare") {
    const { vs } = await props.searchParams;
    return (
      <DocsPage
        full
        className="max-w-none"
        tableOfContent={{ enabled: false }}
        tableOfContentPopover={{ enabled: false }}
        footer={{ className: "mx-auto w-full max-w-4xl" }}
      >
        <DocsBody className="mx-auto w-full max-w-4xl pb-12">
          <MDX
            components={{
              ...components,
              CompareWith: () => <CompareWith initial={typeof vs === "string" ? vs : undefined} />,
            }}
          />
        </DocsBody>
      </DocsPage>
    );
  }

  return (
    <DocsPage toc={page.data.toc} full={page.data.full} tableOfContent={{ style: "clerk" }}>
      <DocsTitle>{page.data.title}</DocsTitle>
      <DocsDescription className="mb-0">{page.data.description}</DocsDescription>
      <div className="flex items-center gap-2">
        <MarkdownCopyButton markdownUrl={`${page.url}.md`} />
        <ViewOptionsPopover
          markdownUrl={`${page.url}.md`}
          githubUrl={`https://github.com/ocelhq/ocel/blob/main/www/content/docs/${page.path}`}
        />
      </div>
      <DocsBody>
        <MDX components={components} />
      </DocsBody>
    </DocsPage>
  );
}

export async function generateStaticParams() {
  return source.generateParams();
}

export async function generateMetadata(props: PageProps<"/docs/[[...slug]]">): Promise<Metadata> {
  const params = await props.params;
  const page = source.getPage(params.slug);
  if (!page) notFound();

  return {
    title: page.url === "/docs" ? { absolute: "Ocel Docs" } : page.data.title,
    description: page.data.description,
  };
}

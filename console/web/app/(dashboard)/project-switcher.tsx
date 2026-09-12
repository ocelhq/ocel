"use client";

import { CaretUpDownIcon, XIcon } from "@phosphor-icons/react";
import Link from "next/link";
import { useParams, usePathname, useRouter, useSearchParams } from "next/navigation";
import { useRef } from "react";
import {
  Combobox,
  ComboboxContent,
  ComboboxEmpty,
  ComboboxInput,
  ComboboxItem,
  ComboboxList,
  ComboboxSeparator,
  ComboboxTrigger,
} from "@/components/ui/combobox";
import { environmentOf } from "@/lib/environment";
import { EnvironmentSwitcher } from "./environment-switcher";
import { projectHref, scopeHref, sectionOf } from "./sections";

export type ProjectOption = { name: string; slug: string };

const iconButton =
  "grid size-7 shrink-0 place-items-center text-muted-foreground outline-hidden transition-colors hover:bg-muted hover:text-foreground focus-visible:ring-1 focus-visible:ring-ring";

const searchInputClass =
  "m-0! h-11! border-0! bg-transparent! p-0 shadow-none! outline-none! has-[[data-slot=input-group-control]:focus-visible]:border-0! has-[[data-slot=input-group-control]:focus-visible]:ring-0! **:data-[slot=input-group-control]:h-11 **:data-[slot=input-group-control]:px-3.5 **:data-[slot=input-group-control]:py-3 **:data-[slot=input-group-control]:text-sm **:data-[slot=input-group-control]:leading-5 **:data-[slot=input-group-control]:focus-visible:border-0! **:data-[slot=input-group-control]:focus-visible:ring-0!";

export function ProjectSwitcher({ projects }: { projects: ProjectOption[] | null }) {
  const router = useRouter();
  const { slug } = useParams<{ slug?: string }>();
  const section = sectionOf(usePathname()) ?? "";
  const environment = environmentOf(useSearchParams().get("env"));
  const anchorRef = useRef<HTMLLIElement>(null);
  const triggerAnchorRef = useRef<HTMLDivElement>(null);

  if (!projects) {
    return <span className="font-medium">All projects</span>;
  }

  const active = projects.find((item) => item.slug === slug) ?? null;

  return (
    <Combobox
      items={projects}
      value={active}
      onValueChange={(next) => {
        if (next) {
          router.push(projectHref(next.slug, section, environment));
        }
      }}
      itemToStringLabel={(item) => item.name}
    >
      {active ? (
        <nav aria-label="Breadcrumb" className="-ml-2 flex h-9 min-w-0 items-center">
          <ol className="flex min-w-0 items-center gap-1">
            <li ref={anchorRef} className="flex min-w-0 items-center gap-0.5">
              <Link
                href={projectHref(active.slug, "", environment)}
                className="truncate px-2 py-1 font-mono text-[13px] font-medium outline-hidden transition-colors hover:bg-muted focus-visible:ring-1 focus-visible:ring-ring"
              >
                {active.slug}
              </Link>
              <ComboboxTrigger
                aria-label="Switch project"
                className={`${iconButton} [&>svg:last-child]:hidden`}
              >
                <CaretUpDownIcon className="size-4" />
              </ComboboxTrigger>
            </li>
            <li aria-hidden="true" className="shrink-0 font-mono text-[13px] text-dim select-none">
              /
            </li>
            <li className="flex shrink-0 items-center gap-0.5">
              <EnvironmentSwitcher />
              <Link
                href={scopeHref(section)}
                aria-label="Close project"
                title="Close project"
                className={iconButton}
              >
                <XIcon className="size-3.5" />
              </Link>
            </li>
          </ol>
        </nav>
      ) : (
        <div ref={triggerAnchorRef} className="-ml-2 flex h-9 min-w-0 items-center">
          <ComboboxTrigger className="flex h-8 items-center gap-2 px-2 font-medium outline-hidden transition-colors hover:bg-muted focus-visible:ring-1 focus-visible:ring-ring data-popup-open:bg-muted [&>svg:last-child]:hidden">
            All projects
            <CaretUpDownIcon className="size-4 text-muted-foreground" />
          </ComboboxTrigger>
        </div>
      )}
      <ComboboxContent anchor={active ? anchorRef : triggerAnchorRef} className="w-80">
        <ComboboxInput
          showTrigger={false}
          placeholder="Search projects..."
          aria-label="Search projects"
          className={searchInputClass}
        />
        <ComboboxSeparator />
        <ComboboxEmpty className="py-6">No projects found.</ComboboxEmpty>
        <ComboboxList className="p-1.5">
          {(item: ProjectOption) => (
            <ComboboxItem key={item.slug} value={item} className="py-2.5 pl-3 text-sm leading-5">
              <span className="flex min-w-0 flex-col gap-0.5">
                <span className="truncate">{item.name}</span>
                <span className="truncate font-mono text-xs text-muted-foreground">
                  {item.slug}
                </span>
              </span>
            </ComboboxItem>
          )}
        </ComboboxList>
      </ComboboxContent>
    </Combobox>
  );
}

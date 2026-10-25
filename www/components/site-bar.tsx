// TODO: cloud/www/components/site-bar.tsx is a copy of this file; move both into a shared package.

import type { SVGProps } from "react";
import { cn } from "@/lib/utils";

const cutRing = "M66.94 2.96 A50 50 0 1 0 98.62 38.33 L72.36 44.63 A23 23 0 1 1 57.79 28.36 Z";

const github =
  "M512 0C229.12 0 0 229.12 0 512c0 226.56 146.56 417.92 350.08 485.76 25.6 4.48 35.2-10.88 35.2-24.32 0-12.16-.64-52.48-.64-95.36-128.64 23.68-161.92-31.36-172.16-60.16-5.76-14.72-30.72-60.16-52.48-72.32-17.92-9.6-43.52-33.28-.64-33.92 40.32-.64 69.12 37.12 78.72 52.48 46.08 77.44 119.68 55.68 149.12 42.24 4.48-33.28 17.92-55.68 32.64-68.48-113.92-12.8-232.96-56.96-232.96-252.8 0-55.68 19.84-101.76 52.48-137.6-5.12-12.8-23.04-65.28 5.12-135.68 0 0 42.88-13.44 140.8 52.48 40.96-11.52 84.48-17.28 128-17.28s87.04 5.76 128 17.28c97.92-66.56 140.8-52.48 140.8-52.48 28.16 70.4 10.24 122.88 5.12 135.68 32.64 35.84 52.48 81.28 52.48 137.6 0 196.48-119.68 240-233.6 252.8 18.56 16 34.56 46.72 34.56 94.72 0 68.48-.64 123.52-.64 140.8 0 13.44 9.6 29.44 35.2 24.32C877.44 929.92 1024 737.92 1024 512 1024 229.12 794.88 0 512 0";

const sites = {
  engine: "https://ocel.dev",
  cloud: "https://ocelhq.com",
  console: "https://app.ocelhq.com",
  github: "https://github.com/ocelhq/ocel",
};

type Site = "engine" | "cloud";

const cell = "relative flex items-center focus-visible:-outline-offset-3!";

function Mark(props: SVGProps<SVGSVGElement>) {
  return (
    <svg viewBox="0 0 100 100" aria-hidden="true" {...props}>
      <path fill="var(--electric)" d={cutRing} />
    </svg>
  );
}

function Switch({ current }: { current: Site }) {
  const door = (site: Site, label: string) => {
    const here = site === current;
    return (
      <a
        href={here ? "/" : sites[site]}
        aria-current={here ? "page" : undefined}
        className={cn(
          cell,
          "px-2.5 font-mono text-[11px] font-medium tracking-[0.14em] uppercase transition-colors sm:px-4",
          here ? "text-foreground" : "text-muted-foreground hover:bg-muted hover:text-foreground",
        )}
      >
        {label}
        {here ? (
          <span aria-hidden className="absolute inset-x-0 -bottom-[1.5px] h-[3px] bg-electric" />
        ) : null}
      </a>
    );
  };

  return (
    <div className="flex items-stretch divide-x divide-border border-border sm:border-x">
      {door("engine", "Engine")}
      {door("cloud", "Cloud")}
    </div>
  );
}

export function SiteBar({ current }: { current: Site }) {
  const link =
    "px-3 text-[14px] font-medium text-muted-foreground transition-colors hover:text-foreground";

  return (
    <header className="sticky top-0 z-50 border-b-[1.5px] border-foreground bg-background/85 backdrop-blur-md">
      <nav
        aria-label="Ocel"
        className="mx-auto flex h-14 max-w-[1180px] items-stretch px-5 md:px-10"
      >
        <a
          href={current === "engine" ? "/" : sites.engine}
          aria-label="ocel, home"
          className={cn(cell, "gap-[9px] pr-3 sm:pr-6")}
        >
          <Mark className="size-5 flex-none" />
          <span className="hidden font-display text-[1.475rem] leading-none font-extrabold tracking-[-0.04em] text-foreground lowercase min-[360px]:inline">
            ocel
          </span>
          <span className="ml-1 hidden bg-(--warn) px-1 py-[3px] font-mono text-[10px] leading-none font-semibold tracking-[0.14em] text-(--paper) uppercase sm:inline">
            Alpha
          </span>
        </a>

        <Switch current={current} />

        <div className="ml-auto flex items-stretch">
          <a href={`${sites.engine}/docs`} className={cn(cell, link, "hidden sm:flex")}>
            Docs
          </a>
          <a
            href={sites.github}
            className={cn(cell, link, "hidden gap-2 min-[380px]:flex sm:pr-4")}
          >
            <svg
              viewBox="0 0 1024 1024"
              aria-hidden="true"
              className="size-4 flex-none fill-current"
            >
              <path fillRule="evenodd" clipRule="evenodd" d={github} />
            </svg>
            <span className="sr-only md:not-sr-only">GitHub</span>
          </a>
          <a
            href={sites.console}
            className={cn(
              cell,
              "gap-2 border-x-[1.5px] border-foreground px-3 text-[14px] font-medium whitespace-nowrap text-foreground transition-colors hover:bg-foreground hover:text-background sm:px-4",
            )}
          >
            Sign in
            <svg
              viewBox="0 0 12 12"
              aria-hidden="true"
              className="hidden size-3 flex-none sm:block"
            >
              <path
                d="M1 6h9M6.5 2.5 10 6l-3.5 3.5"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.5"
              />
            </svg>
          </a>
        </div>
      </nav>
    </header>
  );
}

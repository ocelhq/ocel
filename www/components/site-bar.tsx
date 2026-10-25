// TODO: cloud/www/components/site-bar.tsx is a copy of this file; move both into a shared package.

import type { SVGProps } from "react";

const cutRing = "M66.94 2.96 A50 50 0 1 0 98.62 38.33 L72.36 44.63 A23 23 0 1 1 57.79 28.36 Z";

const sites = {
  engine: "https://ocel.dev",
  cloud: "https://ocelhq.com",
  console: "https://app.ocelhq.com",
  github: "https://github.com/ocelhq/ocel",
};

function Mark(props: SVGProps<SVGSVGElement>) {
  return (
    <svg viewBox="0 0 100 100" aria-hidden="true" {...props}>
      <path fill="var(--electric)" d={cutRing} />
    </svg>
  );
}

type Site = "engine" | "cloud";

export function SiteBar({ current }: { current: Site }) {
  const home = current === "engine" ? "/" : sites.engine;
  const product = (site: Site, label: string) => {
    const here = site === current;
    return (
      <a
        href={here ? "/" : sites[site]}
        aria-current={here ? "page" : undefined}
        className={
          here
            ? "border-b-[1.5px] border-electric pb-0.5 text-foreground"
            : "border-b-[1.5px] border-transparent pb-0.5 text-muted-foreground transition-colors hover:text-foreground"
        }
      >
        {label}
      </a>
    );
  };

  return (
    <header className="sticky top-0 z-50 border-b-[1.5px] border-foreground bg-background/85 backdrop-blur-md">
      <nav
        aria-label="Ocel"
        className="mx-auto flex h-14 max-w-[1180px] items-center gap-3.5 px-5 text-[13px] font-medium sm:gap-5 md:gap-7 md:px-10"
      >
        <a href={home} className="flex items-center gap-[9px]" aria-label="ocel, home">
          <Mark className="size-5 flex-none" />
          <span className="font-display text-[1.475rem] leading-none font-extrabold tracking-[-0.04em] text-foreground lowercase">
            ocel
          </span>
        </a>
        <span className="flex items-center gap-3.5 sm:gap-4">
          {product("engine", "Engine")}
          {product("cloud", "Cloud")}
        </span>
        <a
          href={`${sites.engine}/docs`}
          className="hidden text-muted-foreground transition-colors hover:text-foreground sm:block"
        >
          Docs
        </a>
        <a
          href={sites.github}
          className="text-muted-foreground transition-colors hover:text-foreground"
        >
          GitHub
        </a>
        <span className="ml-auto flex items-center gap-5">
          <span className="hidden font-mono text-[11px] tracking-[0.14em] text-muted-foreground uppercase md:inline">
            Alpha
          </span>
          <a
            href={sites.console}
            className="border-[1.5px] border-foreground px-2.5 py-1.5 whitespace-nowrap text-foreground transition-colors hover:bg-muted sm:px-3"
          >
            Sign in
          </a>
        </span>
      </nav>
    </header>
  );
}

import type { Icon } from "@phosphor-icons/react";
import {
  CheckCircleIcon,
  CircleHalfIcon,
  CircleIcon,
} from "@phosphor-icons/react/dist/ssr";

type Column = {
  label: string;
  dot: string;
  icon: Icon;
  iconClass: string;
  items: string[];
};

const columns: Column[] = [
  {
    label: "COMPLETED",
    dot: "bg-chart-2",
    icon: CheckCircleIcon,
    iconClass: "text-chart-2",
    items: [
      "Zero-config CLI deploys",
      "SDK primitives — postgres, bucket, queue",
      "ocel dev — real cloud sandboxes",
      "Preview environments per branch",
      "One-command rollback",
    ],
  },
  {
    label: "IN PROGRESS",
    dot: "bg-primary animate-pulse motion-reduce:animate-none",
    icon: CircleHalfIcon,
    iconClass: "text-primary",
    items: [
      "Cron & workflow primitives",
      "Containers as a deploy target",
      "Console — live logs & metrics",
    ],
  },
  {
    label: "PLANNED",
    dot: "border border-dim",
    icon: CircleIcon,
    iconClass: "text-dim",
    items: [
      "GCP provider parity",
      "Team access & scoped roles",
      "Interop plugin ecosystem",
    ],
  },
];

export function Roadmap() {
  return (
    <section className="relative overflow-hidden border-t border-border">
      <span className="absolute right-10 top-11 font-mono text-[15px] text-faint">
        +
      </span>
      <span className="pointer-events-none absolute -right-6 top-1/2 hidden origin-center rotate-90 font-mono text-[10.5px] tracking-[0.14em] text-dim md:block">
        FIG. 03 — ROADMAP
      </span>
      <div className="mx-auto max-w-[1180px] px-5 pb-24 pt-16 md:px-10 md:pb-[116px] md:pt-[84px]">
        <div className="mb-3.5 font-mono text-xs tracking-[0.08em] text-primary">
          ROADMAP — IN THE OPEN
        </div>
        <h2 className="max-w-[24ch] text-[34px] font-semibold leading-[1.15] tracking-[-0.02em] text-foreground">
          We ship fast — and in public.
        </h2>
        <p className="mt-4 max-w-[54ch] text-[15px] leading-[1.65] text-muted-foreground">
          Open source means you don't take the roadmap on faith. Here's what's
          shipped, what's in flight, and what's next — updated as we commit.
        </p>

        <div className="mt-[34px] grid grid-cols-1 border-[1.5px] border-foreground md:grid-cols-3">
          {columns.map((col, i) => {
            const IconMark = col.icon;
            return (
              <div
                key={col.label}
                className={
                  i < 2
                    ? "border-b border-border md:border-b-0 md:border-r"
                    : ""
                }
              >
                <header className="flex items-center gap-2.5 border-b border-border px-6 py-3.5">
                  <span className={`size-[9px] rounded-full ${col.dot}`} />
                  <span className="font-mono text-[11px] tracking-[0.1em] text-dim">
                    {col.label}
                  </span>
                  <span className="ml-auto font-mono text-[11px] text-dim">
                    {col.items.length.toString().padStart(2, "0")}
                  </span>
                </header>
                <ul>
                  {col.items.map((item, j) => (
                    <li
                      key={item}
                      className={
                        "flex items-start gap-3 px-6 py-3.5 text-sm leading-[1.5] text-foreground" +
                        (j < col.items.length - 1
                          ? " border-b border-border"
                          : "")
                      }
                    >
                      <IconMark
                        weight={col.label === "PLANNED" ? "regular" : "fill"}
                        className={`mt-px size-[17px] shrink-0 ${col.iconClass}`}
                      />
                      <span>{item}</span>
                    </li>
                  ))}
                </ul>
              </div>
            );
          })}
        </div>

        <a
          href="https://github.com/ocel/ocel"
          className="mt-5 inline-block font-mono text-[13px] text-primary hover:underline"
        >
          → track the full board on github
        </a>
      </div>
    </section>
  );
}

import type { SVGProps } from "react";

const cutRing = "M66.94 2.96 A50 50 0 1 0 98.62 38.33 L72.36 44.63 A23 23 0 1 1 57.79 28.36 Z";

export function Mark(props: SVGProps<SVGSVGElement>) {
  return (
    <svg viewBox="0 0 100 100" aria-hidden="true" {...props}>
      <path fill="var(--electric)" d={cutRing} />
    </svg>
  );
}

export function Lockup() {
  return (
    <span className="inline-flex h-[1.875rem] items-center gap-[9px] leading-none">
      <Mark className="size-5 flex-none" />
      <span className="font-(family-name:--font-archivo) text-[23.6px] font-extrabold lowercase leading-none tracking-[-0.04em] text-(--ink)">
        ocel
      </span>
      <span className="ms-[9px] border border-(--steel) px-1.5 py-1 font-mono text-[0.6875rem] font-medium uppercase leading-none tracking-[0.14em] text-(--steel)">
        Docs
      </span>
    </span>
  );
}

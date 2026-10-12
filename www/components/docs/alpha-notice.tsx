import type { SVGProps } from "react";

function BarrierIcon(props: SVGProps<SVGSVGElement>) {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.5}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      {...props}
    >
      <rect x="2.25" y="6" width="19.5" height="7" />
      <path d="M6 6 2.25 9.75M11 6 6 13M16 6l-5 7M21 6.75 16 13M21.75 10.5 19.5 13" />
      <path d="M5.25 13v6.75M18.75 13v6.75M3.75 19.75h3M17.25 19.75h3" />
      <path d="M5.25 6V4.25M18.75 6V4.25" />
    </svg>
  );
}

export function AlphaNotice() {
  return (
    <aside className="not-prose my-8 flex gap-3 border border-(--hairline) border-s-2 border-s-(--warn) px-4 py-3.5">
      <BarrierIcon className="mt-0.5 size-5 shrink-0 text-(--warn)" />
      <p className="text-[0.9375rem] leading-[1.6] text-(--body)">
        <span className="font-medium text-(--ink)">Ocel is in early alpha, so expect bugs.</span> If
        you hit one,{" "}
        <a
          href="https://github.com/ocelhq/ocel/issues/new/choose"
          className="border-b-[1.5px] border-(--electric) text-(--ink) no-underline"
        >
          open an issue
        </a>
        .
      </p>
    </aside>
  );
}

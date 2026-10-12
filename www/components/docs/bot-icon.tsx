import type { SVGProps } from "react";

export function BotIcon(props: SVGProps<SVGSVGElement>) {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      fill="none"
      viewBox="0 0 24 24"
      strokeWidth={1.5}
      stroke="currentColor"
      aria-hidden="true"
      {...props}
    >
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M12 4.5V7.5M12 4.5a.75.75 0 1 0 0-1.5.75.75 0 0 0 0 1.5ZM6.75 7.5h10.5a2.25 2.25 0 0 1 2.25 2.25v7.5a2.25 2.25 0 0 1-2.25 2.25H6.75A2.25 2.25 0 0 1 4.5 17.25v-7.5A2.25 2.25 0 0 1 6.75 7.5ZM2.25 12v3M21.75 12v3M9 12v1.5M15 12v1.5M9.75 16.5h4.5"
      />
    </svg>
  );
}

import type { BaseLayoutProps } from "fumadocs-ui/layouts/shared";
import { GithubIcon } from "@/components/github-icon";
import { Lockup } from "@/components/logo";

export function baseOptions(): BaseLayoutProps {
  return {
    nav: {
      title: <Lockup />,
    },
    links: [
      {
        type: "icon",
        text: "GitHub",
        url: "https://github.com/ocelhq/ocel",
        icon: <GithubIcon />,
        external: true,
      },
    ],
  };
}

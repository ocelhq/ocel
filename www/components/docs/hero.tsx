import { highlight } from "fumadocs-core/highlight";
import Link from "next/link";
import type { ReactNode } from "react";
import { InstallCommand, type InstallMethod } from "@/components/docs/install-command";
import { OverviewDiagram } from "@/components/docs/overview-diagram";

const inline = "border-b-[1.5px] border-(--electric) text-(--ink) no-underline";

const COMMANDS = [
  { name: "curl", command: "curl -fsSL https://ocel.dev/install.sh | sh" },
  { name: "brew", command: "brew install --cask ocelhq/tap/ocel" },
  { name: "npm", command: "npm install -g @ocel/cli" },
];

function Bare({ children }: { children?: ReactNode }) {
  return <>{children}</>;
}

async function installMethods(): Promise<InstallMethod[]> {
  return Promise.all(
    COMMANDS.map(async (method) => ({
      ...method,
      highlighted: await highlight(method.command, {
        lang: "bash",
        themes: { light: "github-light", dark: "github-dark" },
        defaultColor: false,
        components: { pre: Bare, code: Bare },
      }),
    })),
  );
}

export async function Hero() {
  return (
    <section className="not-prose flex flex-col gap-8 border-b border-(--hairline) pt-4 pb-12 lg:flex-row lg:items-center lg:justify-between lg:gap-12">
      <div className="min-w-0 max-w-2xl flex-1">
        <h1 className="text-balance font-display text-[2.5rem] font-semibold leading-[1.1] tracking-[-0.035em] text-(--ink)">
          Ocel Documentation
        </h1>
        <p className="mt-5 max-w-[34ch] text-lg leading-[1.55] text-(--body)">
          Ocel deploys{" "}
          <Link href="/docs/frameworks/nextjs" className={inline}>
            apps
          </Link>{" "}
          to your own{" "}
          <Link href="/docs/providers/aws" className={inline}>
            cloud
          </Link>{" "}
          or servers with one command.
        </p>
        <InstallCommand methods={await installMethods()} />
        <p className="mt-4 text-[0.9375rem] text-(--body)">
          Then follow the{" "}
          <Link href="/docs/quick-start" className={inline}>
            quick start
          </Link>
          .
        </p>
      </div>
      <OverviewDiagram className="order-first w-full max-w-[24rem] shrink-0 self-start lg:order-none lg:self-auto" />
    </section>
  );
}

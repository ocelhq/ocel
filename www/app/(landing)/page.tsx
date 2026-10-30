import type { ReactNode } from "react";
import { codeToHtml } from "shiki";
import { Clip } from "@/components/landing/clip";
import { clips } from "@/components/landing/clips";
import { CopyCommand } from "@/components/landing/copy-command";
import { DeployDiagram } from "@/components/landing/deploy-diagram";
import { EarlyAccess } from "@/components/landing/early-access";
import { languages, primitives, shikiLang } from "@/components/landing/primitives";
import { PrimitivesGrid, type RenderedPrimitive } from "@/components/landing/primitives-grid";
import { type RenderedTarget, TargetConfig } from "@/components/landing/target-config";
import { targets } from "@/components/landing/targets";
import { SiteBar } from "@/components/site-bar";
import { cn } from "@/lib/utils";

const install = "curl -fsSL https://ocel.dev/install.sh | sh";
const repo = "https://github.com/ocelhq/ocel";
const consoleRepo = "https://github.com/ocelhq/console";
const consoleUrl = "https://app.ocelhq.com";

async function highlight(code: string, lang: string) {
  const html = await codeToHtml(code, { lang, theme: "vitesse-dark" });
  return html.replace(/background-color:[^;"]*;?/g, "");
}

async function renderPrimitives(): Promise<RenderedPrimitive[]> {
  return Promise.all(
    primitives.map(async (primitive) => {
      const entries = await Promise.all(
        languages.map(async (language) => {
          const { declare, use } = primitive.snippets[language];
          const lang = shikiLang[language];
          const [declared, used] = await Promise.all([
            highlight(declare.code, lang),
            highlight(use.code, lang),
          ]);
          return [
            language,
            {
              declare: { file: declare.file, html: declared },
              use: { file: use.file, html: used },
            },
          ] as const;
        }),
      );
      return {
        name: primitive.name,
        what: primitive.what,
        docs: primitive.docs,
        code: Object.fromEntries(entries) as RenderedPrimitive["code"],
      };
    }),
  );
}

function Container({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div className={cn("mx-auto w-full max-w-[1180px] px-5 md:px-10", className)}>{children}</div>
  );
}

function Sheet({
  children,
  className,
  tone = "paper",
  id,
}: {
  children: ReactNode;
  className?: string;
  tone?: "paper" | "fog";
  id?: string;
}) {
  return (
    <section
      id={id}
      className={cn(
        "relative border-t-[1.5px] border-rule py-16 md:py-24",
        tone === "fog" && "bg-muted",
        className,
      )}
    >
      <Container className="relative">{children}</Container>
    </section>
  );
}

function Heading({ children, lede }: { children: ReactNode; lede: ReactNode }) {
  return (
    <div className="mb-10 md:mb-14">
      <h2 className="m-0 max-w-[16ch] text-[clamp(2.25rem,5vw,3.75rem)] leading-[1.02] font-semibold tracking-[-0.035em] text-balance text-foreground">
        {children}
      </h2>
      <p className="m-0 mt-5 max-w-[52ch] text-[17px] leading-[1.6] text-pretty text-muted-foreground">
        {lede}
      </p>
    </div>
  );
}

function C({ children }: { children: ReactNode }) {
  return <code className="font-mono text-[0.9em] text-foreground">{children}</code>;
}

function Underlined({ href, children }: { href: string; children: ReactNode }) {
  return (
    <a
      href={href}
      className="w-fit border-b-[1.5px] border-electric font-mono text-[13px] text-foreground"
    >
      {children}
    </a>
  );
}

export default async function Home() {
  const [renderedTargets, rendered] = await Promise.all([
    Promise.all(
      targets.map(
        async (target): Promise<RenderedTarget> => ({
          id: target.id,
          label: target.label,
          output: target.output,
          html: await highlight(target.config, "typescript"),
        }),
      ),
    ),
    renderPrimitives(),
  ]);

  return (
    <div className="min-h-screen bg-background">
      <SiteBar />
      <main>
        <section className="relative overflow-hidden pt-14 pb-16 md:pt-20 md:pb-24">
          <div aria-hidden className="paper-grid absolute inset-0" />
          <Container className="relative">
            <h1 className="m-0 text-[clamp(3rem,8vw,6rem)] leading-[0.98] font-semibold tracking-[-0.04em] text-balance text-foreground">
              Deploy apps{" "}
              <span className="underline decoration-electric decoration-[6px] underline-offset-[12px]">
                anywhere.
              </span>
            </h1>
            <p className="m-0 mt-7 max-w-[54ch] text-[18px] leading-[1.55] text-pretty text-muted-foreground md:text-[19px]">
              One CLI ships your app to AWS, Google Cloud, a VPS or Ocel Cloud. Your code declares
              the{" "}
              <a
                href="#resources"
                className="text-foreground underline decoration-electric decoration-[1.5px] underline-offset-4"
              >
                resources
              </a>{" "}
              it needs, and Ocel builds them too.
            </p>

            <div className="mt-9 flex flex-col gap-3 sm:flex-row sm:items-stretch">
              <div className="w-full sm:max-w-[28rem] [&>button]:h-full [&>button]:items-center">
                <CopyCommand command={install} method="curl" />
              </div>
              <a
                href="/docs/quick-start"
                className="group inline-flex items-center justify-center gap-2.5 border-[1.5px] border-foreground bg-foreground px-5 py-3 text-[15px] font-semibold whitespace-nowrap text-background transition-opacity hover:opacity-85"
              >
                Get started
                <svg
                  viewBox="0 0 12 12"
                  aria-hidden="true"
                  className="size-3 transition-transform group-hover:translate-x-0.5"
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
          </Container>
        </section>

        <Sheet id="cli">
          <Heading
            lede={
              <>
                Point it at a framework app and a target. A config names where; the CLI handles how.
                No infrastructure code.
              </>
            }
          >
            One command. Any target.
          </Heading>
          <div className="grid grid-cols-[minmax(0,1fr)] gap-6 lg:grid-cols-12">
            <figure className="m-0 grid place-items-center border-[1.5px] border-rule bg-muted px-4 py-10 md:px-8 lg:col-span-5">
              <DeployDiagram className="h-auto w-full max-w-[28rem]" />
            </figure>
            <div className="min-w-0 lg:col-span-7">
              <TargetConfig targets={renderedTargets} />
            </div>
          </div>
        </Sheet>

        <Sheet id="resources" tone="fog">
          <Heading
            lede={
              <>
                Declare a resource once, then import it wherever you use it. <C>ocel deploy</C>{" "}
                provisions it on the target, and <C>ocel dev</C> runs it on your machine.
              </>
            }
          >
            Application defined infrastructure.
          </Heading>
          <PrimitivesGrid primitives={rendered} />
          <div className="mt-8 flex flex-col gap-3 border-t border-border pt-6 sm:flex-row sm:items-center sm:gap-5">
            <span className="w-fit bg-terminal px-3 py-1.5 font-mono text-[13px] text-terminal-ink">
              <span className="text-[#7d8590] select-none">$ </span>
              ocel dev -- next dev
            </span>
            <p className="m-0 text-[15px] leading-[1.6] text-muted-foreground">
              Starts what your app declares in local containers, then your app with them. Docker is
              all it needs.
            </p>
          </div>
        </Sheet>

        <Sheet id="console">
          <Heading
            lede={
              <>
                Deployments, variables and your team, wherever each app runs. Use it at
                app.ocelhq.com, or deploy it yourself with <C>ocel deploy</C>: it's an app like
                yours.
              </>
            }
          >
            One dashboard for every target.
          </Heading>
          <div className="flex flex-wrap gap-x-8 gap-y-3 border-t border-border pt-6">
            <Underlined href={consoleUrl}>Sign in at app.ocelhq.com</Underlined>
            <Underlined href={consoleRepo}>Self-host it: github.com/ocelhq/console</Underlined>
          </div>
        </Sheet>

        <Sheet id="cloud" tone="fog">
          <div className="grid grid-cols-[minmax(0,1fr)] items-center gap-10 lg:grid-cols-12">
            <div className="lg:col-span-5">
              <h2 className="m-0 max-w-[14ch] text-[clamp(2.25rem,5vw,3.75rem)] leading-[1.02] font-semibold tracking-[-0.035em] text-balance text-foreground">
                Ocel Cloud, by invite.
              </h2>
              <p className="m-0 mt-5 max-w-[46ch] text-[17px] leading-[1.6] text-pretty text-muted-foreground">
                Log in, deploy, get a URL, with no cloud account of yours. It's in alpha, so we let
                people in a few at a time. Your own AWS, Google Cloud or VPS needs no invite.
              </p>
              <EarlyAccess source="landing" className="mt-8 max-w-[28rem]" />
            </div>
            <div className="min-w-0 lg:col-span-7">
              <Clip clip={clips.console} />
            </div>
          </div>
        </Sheet>
      </main>

      <footer className="border-t-[1.5px] border-rule">
        <Container className="flex flex-wrap items-center gap-x-6 gap-y-3 py-8 text-[13px] text-muted-foreground">
          <span className="font-display text-[15px] font-extrabold tracking-[-0.04em] text-foreground lowercase">
            ocel
          </span>
          <a href="/docs" className="hover:text-foreground">
            Docs
          </a>
          <a href="/docs/cli" className="hover:text-foreground">
            CLI
          </a>
          <a href="/docs/sdk" className="hover:text-foreground">
            SDK
          </a>
          <a href="/docs/agents" className="hover:text-foreground">
            Agents
          </a>
          <a href={repo} className="hover:text-foreground">
            GitHub
          </a>
          <a href={consoleUrl} className="ml-auto hover:text-foreground">
            Console
          </a>
        </Container>
      </footer>
    </div>
  );
}

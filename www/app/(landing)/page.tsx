import type { ComponentType, ReactNode } from "react";
import { codeToHtml } from "shiki";
import { GoLogo, RustLogo } from "@/components/docs/logos";
import { CopyCommand } from "@/components/landing/copy-command";
import { EarlyAccess } from "@/components/landing/early-access";
import { Screen, Session } from "@/components/landing/screen";
import { TargetProvider, TargetSelect } from "@/components/landing/target";
import { ConfigDiff, Needs, TargetName } from "@/components/landing/target-parts";
import {
  ExpressLogo,
  FastifyLogo,
  HonoLogo,
  NestjsLogo,
  NextjsLogo,
  SvelteLogo,
} from "@/components/landing/tool-logos";
import { agent, deploy, dev, preview } from "@/components/landing/transcripts";
import { SiteBar } from "@/components/site-bar";
import { cn } from "@/lib/utils";

const install = "curl -fsSL https://ocel.dev/install.sh | sh";
const repo = "https://github.com/ocelhq/ocel";

const resourcesCode = `import { bucket } from "ocel/bucket";
import { postgres } from "ocel/postgres";
import { task } from "ocel/task";

export const db = postgres("main");
export const uploads = bucket("uploads");
export const digest = task("weekly-digest", {
  run: async () => sendDigest(await db.query("select * from notes")),
});`;

const pageCode = `import { db } from "@/resources";

export default async function Notes() {
  const { rows } = await db.query("select id, body from notes");
  return rows.map((note) => <p key={note.id}>{note.body}</p>);
}`;

const heroCode = `import { postgres } from "ocel/postgres";

export const db = postgres("main");`;

const pageHeroCode = `import { db } from "@/resources";

const { rows } = await db.query(
  "select body from notes",
);`;

const frameworks: { name: string; Logo: ComponentType<{ className?: string }> }[] = [
  { name: "Next.js", Logo: NextjsLogo },
  { name: "SvelteKit", Logo: SvelteLogo },
  { name: "Express", Logo: ExpressLogo },
  { name: "Fastify", Logo: FastifyLogo },
  { name: "Hono", Logo: HonoLogo },
  { name: "NestJS", Logo: NestjsLogo },
  { name: "Go", Logo: GoLogo },
  { name: "Rust", Logo: RustLogo },
];

const resources = ["postgres", "bucket", "kv", "task", "topic", "worker", "realtime"];

const places = [
  {
    name: "Ocel Cloud",
    status: "Early access",
    where: "Ocel's machines. Apps sleep when idle and wake on the next request.",
    bring: "Nothing",
  },
  {
    name: "AWS",
    where: "Your account, under your billing and access.",
    bring: "Credentials in your shell",
  },
  {
    name: "Google Cloud",
    where: "Your project, under your billing and access.",
    bring: "Application-default credentials",
  },
  {
    name: "VPS",
    where: "Any Linux box you own.",
    bring: "SSH access",
  },
  {
    name: "Cloudflare",
    where: "An edge in front of any of the above.",
    bring: "An API token",
  },
];

async function highlight(code: string, lang: "tsx" | "typescript") {
  const html = await codeToHtml(code, { lang, theme: "vitesse-dark" });
  return html.replace(/background-color:[^;"]*;?/g, "");
}

function Container({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <div className={cn("mx-auto w-full max-w-[1180px] px-5 md:px-10", className)}>{children}</div>
  );
}

function Crosses() {
  return (
    <>
      {["top-3 left-3", "top-3 right-3", "bottom-3 left-3", "bottom-3 right-3"].map((at) => (
        <span
          key={at}
          aria-hidden
          className={cn(
            "absolute hidden font-mono text-[13px] leading-none text-faint md:block",
            at,
          )}
        >
          +
        </span>
      ))}
    </>
  );
}

function Caption({ children }: { children: ReactNode }) {
  return (
    <p className="m-0 mt-4 flex items-start gap-3 font-mono text-[11px] leading-[1.6] tracking-[0.14em] text-muted-foreground uppercase">
      <span className="mt-[0.75em] h-[1.5px] w-6 shrink-0 bg-foreground" />
      {children}
    </p>
  );
}

function Sheet({
  children,
  className,
  tone = "paper",
}: {
  children: ReactNode;
  className?: string;
  tone?: "paper" | "fog";
}) {
  return (
    <section
      className={cn(
        "relative border-t-[1.5px] border-foreground py-16 md:py-24",
        tone === "fog" && "bg-muted",
        className,
      )}
    >
      <Crosses />
      <Container className="relative">{children}</Container>
    </section>
  );
}

function Heading({ children, lede }: { children: ReactNode; lede: ReactNode }) {
  return (
    <div className="mb-10 grid gap-5 md:mb-14 md:grid-cols-12 md:items-end">
      <h2 className="m-0 text-[clamp(1.875rem,3.6vw,2.75rem)] leading-[1.06] font-semibold tracking-[-0.03em] text-balance text-foreground md:col-span-7">
        {children}
      </h2>
      <p className="m-0 max-w-[46ch] text-[16px] leading-[1.6] text-pretty text-muted-foreground md:col-span-5">
        {lede}
      </p>
    </div>
  );
}

function C({ children }: { children: ReactNode }) {
  return <code className="font-mono text-[0.9em] text-foreground">{children}</code>;
}

function Code({ html, file }: { html: string; file: string }) {
  return (
    <div className="flex min-w-0 flex-col">
      <div className="border-b border-terminal-rule px-4 py-2 font-mono text-[11px] text-terminal-foreground">
        {file}
      </div>
      <div className="shiki-pane px-4 py-3.5" dangerouslySetInnerHTML={{ __html: html }} />
    </div>
  );
}

function Cell({
  label,
  children,
  className,
}: {
  label: string;
  children: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("flex min-w-0 flex-col gap-3 p-5 md:p-6", className)}>
      <span className="font-mono text-[11px] tracking-[0.14em] text-muted-foreground uppercase">
        {label}
      </span>
      {children}
    </div>
  );
}

function TitleBlock({ source, inputId }: { source: string; inputId?: string }) {
  return (
    <div className="grid border-[1.5px] border-foreground bg-background md:grid-cols-12">
      <Cell label="Ocel Cloud · early access" className="md:col-span-5">
        <p className="m-0 text-[14px] leading-relaxed text-muted-foreground">
          The quickest target: no account of yours, no keys. Invites go out in batches.
        </p>
        <EarlyAccess source={source} inputId={inputId} />
      </Cell>
      <Cell
        label="Install the CLI"
        className="border-t border-border md:col-span-4 md:border-t-0 md:border-l"
      >
        <p className="m-0 text-[14px] leading-relaxed text-muted-foreground">
          One binary. Your own cloud works today, without an invite.
        </p>
        <CopyCommand command={install} />
      </Cell>
      <Cell
        label="Source"
        className="border-t border-border md:col-span-3 md:border-t-0 md:border-l"
      >
        <p className="m-0 text-[14px] leading-relaxed text-muted-foreground">
          The engine, the SDKs and the console, in the open.
        </p>
        <a
          href={repo}
          className="w-fit border-b-[1.5px] border-electric font-mono text-[13px] text-foreground"
        >
          github.com/ocelhq/ocel
        </a>
        <span className="font-mono text-[11px] tracking-[0.14em] text-muted-foreground uppercase">
          Apache-2.0
        </span>
      </Cell>
    </div>
  );
}

export default async function Home() {
  const [hero, pageHero, resourcesHtml, pageHtml] = await Promise.all([
    highlight(heroCode, "typescript"),
    highlight(pageHeroCode, "tsx"),
    highlight(resourcesCode, "typescript"),
    highlight(pageCode, "tsx"),
  ]);

  return (
    <TargetProvider>
      <div className="min-h-screen bg-background">
        <SiteBar />
        <main>
          <section className="relative overflow-hidden pt-10 pb-16 md:pt-14 md:pb-24">
            <div aria-hidden className="paper-grid absolute inset-0" />
            <Crosses />
            <Container className="relative">
              <div className="grid gap-6 md:grid-cols-12 md:items-end">
                <h1 className="m-0 text-[clamp(2.5rem,5.4vw,4.25rem)] leading-[1.02] font-semibold tracking-[-0.04em] text-balance text-foreground md:col-span-8">
                  Ocel deploys apps, and{" "}
                  <span className="underline decoration-electric decoration-[5px] underline-offset-[10px]">
                    everything they declare.
                  </span>
                </h1>
                <p className="m-0 max-w-[40ch] text-[17px] leading-[1.55] text-pretty text-muted-foreground md:col-span-4 md:pb-2">
                  Your code declares the database it needs.{" "}
                  <code className="bg-muted px-1.5 font-mono text-[0.85em] text-foreground">
                    ocel deploy
                  </code>{" "}
                  builds the app and the database, on Ocel Cloud or in your own account.
                </p>
              </div>

              <Screen
                className="mt-8 md:mt-10"
                title="~/notes"
                bar={<TargetSelect id="hero-target" />}
              >
                <div className="grid md:grid-cols-12">
                  <div className="order-2 flex flex-col border-t border-terminal-rule md:order-none md:col-span-5 md:border-t-0 md:border-r">
                    <Code html={hero} file="resources/index.ts" />
                    <div className="hidden border-t border-terminal-rule md:block">
                      <Code html={pageHero} file="app/page.tsx" />
                    </div>
                  </div>
                  <Session
                    byTarget={deploy}
                    className="order-1 min-h-[22rem] px-4 py-3 sm:px-5 md:order-none md:col-span-7"
                  />
                </div>
                <Needs />
              </Screen>
              <Caption>
                Fig. 1 · ocel deploy · abridged output; the account, job and URLs are illustrative
              </Caption>

              <div className="mt-6">
                <TitleBlock source="ocel.dev-hero" inputId="early-access" />
              </div>

              <ul className="m-0 mt-8 flex list-none flex-wrap items-center gap-x-6 gap-y-3 p-0 font-mono text-[12.5px] text-muted-foreground">
                <li className="tracking-[0.14em] uppercase">Deploys</li>
                {frameworks.map(({ name, Logo }) => (
                  <li key={name} className="flex items-center gap-2">
                    <Logo className="size-4" />
                    {name}
                  </li>
                ))}
              </ul>
            </Container>
          </section>

          <Sheet>
            <Heading
              lede={
                <>
                  Declare it where you use it.{" "}
                  <code className="font-mono text-[0.9em] text-foreground">ocel dev</code> starts it
                  on your machine, and{" "}
                  <code className="font-mono text-[0.9em] text-foreground">ocel deploy</code>{" "}
                  provisions the same one wherever the app goes. There is no second file to keep in
                  sync.
                </>
              }
            >
              A database is a line of your app.
            </Heading>
            <div className="grid grid-cols-[minmax(0,1fr)] gap-6 lg:grid-cols-12">
              <Screen title="resources/index.ts" className="lg:col-span-7">
                <div
                  className="shiki-pane px-4 py-4"
                  dangerouslySetInnerHTML={{ __html: resourcesHtml }}
                />
              </Screen>
              <Screen title="~/notes" className="lg:col-span-5">
                <Session lines={dev} className="px-4 py-4 sm:px-5" />
              </Screen>
            </div>
            <Caption>Fig. 2 · ocel dev · abridged output</Caption>
            <div className="mt-8 flex flex-wrap items-baseline gap-x-5 gap-y-3 border-t border-border pt-6">
              <span className="font-mono text-[11px] tracking-[0.14em] text-muted-foreground uppercase">
                Resources
              </span>
              {resources.map((name) => (
                <a
                  key={name}
                  href={`/docs/sdk/${name === "worker" ? "task" : name}`}
                  className="font-mono text-[13px] text-foreground underline decoration-border decoration-[1.5px] underline-offset-4 hover:decoration-electric"
                >
                  {name}
                </a>
              ))}
              <span className="text-[14px] text-muted-foreground">
                Docker is all <code className="font-mono text-[0.9em]">ocel dev</code> needs, and
                only when the app declares one.
              </span>
            </div>
          </Sheet>

          <Sheet tone="fog">
            <Heading lede="A preview provisions resources of its own, so a branch never writes to production's database. It's torn down when you're done with it.">
              A URL for the branch you're on.
            </Heading>
            <Screen title="~/notes" bar={<TargetSelect id="preview-target" />}>
              <Session byTarget={preview} className="min-h-[15rem] px-4 py-4 sm:px-5" />
            </Screen>
            <Caption>Fig. 3 · ocel preview up · abridged output; URLs are illustrative</Caption>
          </Sheet>

          <Sheet>
            <Heading lede="Claude Code, Codex or Cursor can declare resources, run ocel dev and ship previews. The CLI is built for them as much as for you.">
              Your agent runs it as well as you do.
            </Heading>
            <div className="grid grid-cols-[minmax(0,1fr)] gap-10 lg:grid-cols-12">
              <dl className="m-0 grid content-start gap-0 lg:col-span-5">
                {(
                  [
                    [
                      "The skill",
                      <>
                        <C>ocel skill install</C> gives the agent the playbook for this CLI's
                        version, and <C>ocel doctor</C> says when it's stale.
                      </>,
                    ],
                    [
                      "JSON everywhere",
                      <>
                        <C>--json</C> on every command: nothing prompts, and every error has a
                        stable code, a hint and a docs URL.
                      </>,
                    ],
                    [
                      "A binary that describes itself",
                      <>
                        <C>ocel help --json</C> lists every command and whether it changes anything;{" "}
                        <C>ocel schema</C> prints its output's shape.
                      </>,
                    ],
                    [
                      "Docs for agents",
                      <>
                        <C>ocel.dev/llms.txt</C> indexes every page, and each page is also served as
                        markdown.
                      </>,
                    ],
                    [
                      "What stays with you",
                      "The skill brings you the plan and waits for your yes on production deploys, rollbacks and anything that costs money.",
                    ],
                  ] as const
                ).map(([term, detail]) => (
                  <div
                    key={term}
                    className="grid gap-1.5 border-t border-border py-4 first:border-t-[1.5px] first:border-electric"
                  >
                    <dt className="text-[15px] font-semibold text-foreground">{term}</dt>
                    <dd className="m-0 text-[14.5px] leading-[1.6] text-muted-foreground">
                      {detail}
                    </dd>
                  </div>
                ))}
              </dl>
              <div className="self-start lg:col-span-7">
                <Screen title="~/notes">
                  <Session lines={agent} className="px-4 py-4 sm:px-5" />
                </Screen>
                <Caption>Fig. 4 · what an agent reads · abridged output</Caption>
              </div>
            </div>
          </Sheet>

          <Sheet tone="fog">
            <Heading
              lede={
                <>
                  The app never names where it runs. Moving it from{" "}
                  <TargetName className="text-foreground" /> to anywhere else is the provider line;
                  the code is the same file it was.
                </>
              }
            >
              Change the target. Not the app.
            </Heading>
            <div className="grid grid-cols-[minmax(0,1fr)] gap-6 lg:grid-cols-12">
              <div className="lg:col-span-5">
                <ConfigDiff />
              </div>
              <Screen
                title={
                  <span>
                    app/page.tsx{" "}
                    <span className="ml-2 text-[#3ecf7a]">0 changes on every target</span>
                  </span>
                }
                className="self-start lg:col-span-7"
              >
                <div
                  className="shiki-pane px-4 py-4"
                  dangerouslySetInnerHTML={{ __html: pageHtml }}
                />
              </Screen>
            </div>
            <Caption>Fig. 5 · the same app, four targets</Caption>

            <div className="mt-12 border-[1.5px] border-foreground bg-background">
              <dl className="m-0 md:hidden">
                {places.map((place) => (
                  <div key={place.name} className="grid gap-1 border-b border-border px-5 py-4">
                    <dt className="font-semibold text-foreground">
                      {place.name}
                      {place.status ? (
                        <span className="ml-2 border border-electric px-1.5 py-0.5 font-mono text-[10.5px] font-medium tracking-[0.14em] text-electric uppercase">
                          {place.status}
                        </span>
                      ) : null}
                    </dt>
                    <dd className="m-0 text-[14.5px] text-muted-foreground">{place.where}</dd>
                    <dd className="m-0 font-mono text-[13px] text-foreground">
                      You bring: {place.bring}
                    </dd>
                  </div>
                ))}
              </dl>
              <table className="hidden w-full border-collapse text-left text-[14.5px] md:table">
                <thead>
                  <tr className="font-mono text-[11px] tracking-[0.14em] text-muted-foreground uppercase">
                    <th className="border-b border-border px-5 py-3 font-medium">Target</th>
                    <th className="border-b border-border px-5 py-3 font-medium">
                      Where the app runs
                    </th>
                    <th className="border-b border-border px-5 py-3 font-medium">What you bring</th>
                  </tr>
                </thead>
                <tbody>
                  {places.map((place) => (
                    <tr key={place.name} className="align-top">
                      <td className="border-b border-border px-5 py-3.5 font-semibold text-foreground">
                        {place.name}
                        {place.status ? (
                          <span className="ml-2 border border-electric px-1.5 py-0.5 font-mono text-[10.5px] font-medium tracking-[0.14em] text-electric uppercase">
                            {place.status}
                          </span>
                        ) : null}
                      </td>
                      <td className="border-b border-border px-5 py-3.5 text-muted-foreground">
                        {place.where}
                      </td>
                      <td className="border-b border-border px-5 py-3.5 font-mono text-[13px] text-foreground">
                        {place.bring}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              <p className="m-0 px-5 py-4 text-[14.5px] text-muted-foreground">
                Already running Terraform, Pulumi or SST? Ocel sits beside them rather than
                replacing them.{" "}
                <a
                  href="/docs/compare"
                  className="border-b-[1.5px] border-electric text-foreground"
                >
                  How it compares
                </a>
              </p>
            </div>
          </Sheet>

          <Sheet>
            <h2 className="m-0 max-w-[18ch] text-[clamp(2.375rem,4.4vw,3.5rem)] leading-[1.05] font-semibold tracking-[-0.035em] text-balance text-foreground">
              Open source, Apache-2.0. Read every line it runs.
            </h2>
            <p className="mt-5 mb-10 max-w-[56ch] text-[16px] leading-[1.6] text-muted-foreground">
              Ocel is pre-release alpha. Nothing is stable yet, and the code makes clean breaks
              while it gets there.
            </p>
            <TitleBlock source="ocel.dev-close" />
          </Sheet>
        </main>

        <footer className="border-t-[1.5px] border-foreground">
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
            <a href="https://ocelhq.com" className="ml-auto hover:text-foreground">
              Ocel Cloud
            </a>
          </Container>
        </footer>
      </div>
    </TargetProvider>
  );
}

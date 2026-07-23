import Image from "next/image";
import Link from "next/link";
import type { ComponentType, ReactNode } from "react";
import { FaAws } from "react-icons/fa";
import { codeToHtml } from "shiki";
import { RdsIcon, S3Icon, SqsIcon } from "@/components/landing/aws-icons";
import { CommandLine } from "@/components/landing/command-line";
import { Hero } from "@/components/landing/hero";
import { Marquee } from "@/components/landing/marquee";
import { Roadmap } from "@/components/landing/roadmap";
import { SiteHeader } from "@/components/landing/site-header";
import { StatusQuo } from "@/components/landing/status-quo";
import { Terminal } from "@/components/landing/terminal";
import {
  AstroLogo,
  ExpressLogo,
  FastifyLogo,
  HonoLogo,
  NestjsLogo,
  NextjsLogo,
  SvelteLogo,
} from "@/components/landing/tool-logos";
import { Wordmark } from "@/components/landing/wordmark";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export default function Home() {
  return (
    <div className="min-h-screen bg-background">
      <SiteHeader />
      <Hero />
      <Marquee />
      <StatusQuo />
      <Cli />
      <Sdk />
      <DevMode />
      <Console />
      <Interop />
      <Pricing />
      <Faq />
      <OneMoreThing />
      <Roadmap />
      <CtaBand />
      <Footer />
    </div>
  );
}

/* ---------------------------------------------------------------- helpers */

function Eyebrow({ children }: { children: ReactNode }) {
  return (
    <div className="mb-3.5 font-mono text-xs tracking-[0.08em] text-primary">
      {children}
    </div>
  );
}

function SectionHeading({ children }: { children: ReactNode }) {
  return (
    <h2 className="text-[34px] font-semibold leading-[1.15] tracking-[-0.02em] text-foreground">
      {children}
    </h2>
  );
}

function Container({ children }: { children: ReactNode }) {
  return (
    <div className="mx-auto max-w-[1180px] px-10 py-[84px]">{children}</div>
  );
}

function DarkCodePane({
  file,
  children,
  className,
}: {
  file: string;
  children: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "self-start border-[1.5px] border-foreground bg-[#0a0a0a] shadow-[6px_6px_0_0_var(--hard-shadow)]",
        className,
      )}
    >
      <div className="border-b border-[#2e2e2e] px-4 py-2.5 font-mono text-[11px] text-[#8a8a8a]">
        {file}
      </div>
      <div className="px-5.5 py-5 font-mono text-[13.5px] leading-[1.95] text-[#e8e9eb]">
        {children}
      </div>
    </div>
  );
}

/* --------------------------------------------------------------- 01 · cli */

type CliCommand = {
  name: string;
  desc: string;
  primary?: boolean;
  note?: string;
};

const cliCommands: CliCommand[] = [
  { name: "init", desc: "create a new project" },
  { name: "dev", desc: "real cloud infra, instantly", note: "later ↓" },
  { name: "deploy", desc: "ship to your account", primary: true },
  { name: "preview", desc: "a URL for the current branch", primary: true },
  { name: "rollback", desc: "revert production, instantly", primary: true },
  { name: "run", desc: "a one-off with your resources" },
];

const frameworks: {
  name: string;
  Logo: ComponentType<{ className?: string }>;
}[] = [
  { name: "Next.js", Logo: NextjsLogo },
  { name: "Astro", Logo: AstroLogo },
  { name: "SvelteKit", Logo: SvelteLogo },
  { name: "Nest.js", Logo: NestjsLogo },
  { name: "Fastify", Logo: FastifyLogo },
  { name: "Express", Logo: ExpressLogo },
  { name: "Hono", Logo: HonoLogo },
];

const cliVerbs = [
  {
    cmd: "$ ocel preview",
    body: "A full environment for the current branch. Share the URL, get the review.",
  },
  {
    cmd: "$ ocel deploy",
    body: "Build, provision, release — phased and observable. Serverless today, containers next.",
  },
  {
    cmd: "$ ocel rollback",
    body: "Production back to any previous deployment. One command, no ceremony.",
  },
];

function Cli() {
  return (
    <section className="relative border-t-[1.5px] border-foreground">
      <span className="absolute right-10 top-11 font-mono text-[15px] text-faint">
        +
      </span>
      <Container>
        <div className="grid grid-cols-1 items-center gap-[52px] md:grid-cols-2">
          <div>
            <Eyebrow>01 — THE CLI</Eyebrow>
            <SectionHeading>
              Works with the framework{" "}
              <span className="text-primary">you already use.</span>
            </SectionHeading>
            <p className="mt-[18px] max-w-[46ch] text-[15px] leading-[1.65] text-muted-foreground">
              Zero-config deploys into the account you already own — the one
              thing everything else is built on. Point it at a project and it
              detects the framework, no config to write.
            </p>
            <div className="mt-[26px] flex flex-wrap items-center gap-2">
              {frameworks.map(({ name, Logo }) => (
                <span
                  key={name}
                  className="inline-flex items-center gap-2 border border-border px-2.5 py-1.5 transition-colors hover:border-foreground"
                >
                  <span className="flex w-4 justify-center text-foreground">
                    <Logo className="h-4 w-auto" />
                  </span>
                  <span className="font-mono text-[12.5px] text-muted-foreground">
                    {name}
                  </span>
                </span>
              ))}
              <span className="font-mono text-[12.5px] text-dim">
                + more added all the time
              </span>
            </div>
            <div className="mt-[26px] flex flex-wrap items-center gap-4">
              <CommandLine command="npm i -g ocel" />
              <a
                href="https://ocel.app/docs/cli/frameworks"
                target="_blank"
                rel="noreferrer"
                className="font-mono text-[13px] text-primary underline-offset-4 hover:underline"
              >
                CLI docs — frameworks ↗
              </a>
            </div>
          </div>
          <Terminal
            title="acme — ocel --help"
            className="self-start"
            bodyClassName="px-5.5 py-5 text-[13px] leading-[1.9] text-foreground"
          >
            <div>
              <span className="text-dim">$</span> ocel --help
            </div>
            <div className="text-dim">ocel deploys apps to your own cloud</div>
            <div className="mt-2 text-dim">Commands:</div>
            {cliCommands.map((cmd) => (
              <div key={cmd.name} className="flex">
                <span className="w-6 shrink-0" />
                <span
                  className={`w-[76px] shrink-0 ${cmd.primary ? "text-primary" : "text-foreground"}`}
                >
                  {cmd.name}
                </span>
                <span className="text-dim">
                  {cmd.desc}
                  {cmd.note ? (
                    <span className="text-faint"> — {cmd.note}</span>
                  ) : null}
                </span>
              </div>
            ))}
          </Terminal>
        </div>
        <div className="mt-[52px] grid grid-cols-1 border-[1.5px] border-foreground md:grid-cols-3">
          {cliVerbs.map((step, i) => (
            <div
              key={step.cmd}
              className={
                i < 2
                  ? "border-b border-border p-6 md:border-b-0 md:border-r"
                  : "p-6"
              }
            >
              <div className="mb-3">
                <span className="inline-block bg-chip px-2 py-[3px] font-mono text-[13px] text-foreground">
                  {step.cmd}
                </span>
              </div>
              <div className="text-sm leading-[1.6] text-muted-foreground">
                {step.body}
              </div>
            </div>
          ))}
        </div>
      </Container>
    </section>
  );
}

/* --------------------------------------------------------------- 02 · sdk */

type SdkResource = {
  handle: string;
  call: string;
  service: string;
  blurb: string;
  Icon: ComponentType<{ className?: string }>;
};

const sdkResources: SdkResource[] = [
  {
    handle: "db",
    call: 'postgres("main")',
    service: "Amazon RDS",
    blurb: "Managed Postgres",
    Icon: RdsIcon,
  },
  {
    handle: "uploads",
    call: 'bucket("uploads")',
    service: "Amazon S3",
    blurb: "Object storage",
    Icon: S3Icon,
  },
  {
    handle: "jobs",
    call: 'queue("emails")',
    service: "Amazon SQS",
    blurb: "Durable queue",
    Icon: SqsIcon,
  },
];

const sdkCode = `import { postgres } from "@ocel/sdk/postgres";
import { bucket } from "@ocel/sdk/blob";
import { queue } from "@ocel/sdk/queue";

export const db = postgres("main");
export const uploads = bucket("uploads");
export const jobs = queue("emails");

// then consume them anywhere
await db.query("select * from orders");
await uploads.put(file);
await jobs.send({ to: user.email });
`;

async function Sdk() {
  const codeHtml = (
    await codeToHtml(sdkCode, { lang: "typescript", theme: "vitesse-dark" })
  ).replace(/background-color:[^;"]*;?/g, "");

  return (
    <section className="border-t border-border bg-secondary">
      <Container>
        <div className="mx-auto max-w-[560px] text-center">
          <Eyebrow>02 — SDK</Eyebrow>
          <SectionHeading>Application-defined infrastructure.</SectionHeading>
          <p className="mx-auto mt-4 max-w-[48ch] text-[15px] leading-[1.65] text-muted-foreground">
            Your app comes first; cloud resources are a consequence of what it
            needs. Call a primitive, use it right there — no separate IaC wiring
            before a line of product code. On deploy, each call lands as real
            infrastructure in your own AWS account.
          </p>
        </div>

        <div className="mt-14 grid grid-cols-1 gap-8 md:grid-cols-[minmax(0,1fr)_72px_minmax(0,1fr)] md:items-center md:gap-x-0">
          <DarkCodePane file="app/ocel/index.ts" className="w-full">
            <div
              className="shiki-pane"
              // biome-ignore lint/security/noDangerouslySetInnerHtml: server-highlighted static code from a trusted local constant
              dangerouslySetInnerHTML={{ __html: codeHtml }}
            />
          </DarkCodePane>

          <div aria-hidden className="hidden items-center md:flex">
            <span className="animate-flow h-[3px] flex-1" />
            <span className="size-0 border-y-[6px] border-l-[8px] border-y-transparent border-l-primary" />
          </div>

          <div className="border-[1.5px] border-foreground bg-card shadow-[6px_6px_0_0_var(--hard-shadow)]">
            <div className="flex items-center gap-2.5 border-b-[1.5px] border-foreground px-4 py-3">
              <FaAws className="size-6 shrink-0 text-foreground" />
              <span className="text-[13px] font-semibold text-foreground">
                Your AWS account
              </span>
              <span className="ml-auto font-mono text-[11px] text-dim">
                provisioned on deploy
              </span>
            </div>
            {sdkResources.map((r) => (
              <div
                key={r.handle}
                className="flex items-center gap-3 border-b border-border px-4 py-3.5"
              >
                <r.Icon className="size-6 shrink-0 text-muted-foreground" />
                <div className="min-w-0">
                  <div className="text-[13px] font-medium text-foreground">
                    {r.service}
                  </div>
                  <div className="text-[12px] text-muted-foreground">
                    {r.blurb}
                  </div>
                </div>
                <span className="ml-auto shrink-0 font-mono text-[11.5px] text-dim">
                  {r.call}
                </span>
              </div>
            ))}
            <div className="flex items-center gap-3 px-4 py-3.5">
              <span
                aria-hidden
                className="size-6 shrink-0 border border-dashed border-dim"
              />
              <div className="min-w-0">
                <div className="text-[13px] font-medium text-muted-foreground">
                  More primitives
                </div>
                <div className="text-[12px] text-dim">
                  cron, workflows &amp; more
                </div>
              </div>
              <span className="ml-auto shrink-0 font-mono text-[11px] text-dim">
                soon
              </span>
            </div>
          </div>
        </div>

        <div className="mt-12 flex justify-center">
          <Button
            variant="outline"
            render={
              <Link
                href="https://ocel.app/docs/sdk"
                target="_blank"
                rel="noreferrer"
              />
            }
            className="h-auto rounded-none border-foreground px-6 py-3 text-[13px] font-semibold normal-case tracking-normal"
          >
            Explore the SDK docs ↗
          </Button>
        </div>
      </Container>
    </section>
  );
}

/* ----------------------------------------------------------- 03 · dev mode */

const devModeTiles: { label: string; visual: ReactNode; copy: string }[] = [
  {
    label: "YOUR WORKFLOW",
    visual: (
      <div className="flex flex-wrap items-center gap-1.5 font-mono text-[12.5px]">
        <span className="bg-chip px-2 py-[3px] text-foreground">ocel dev</span>
        <span className="text-dim">--</span>
        <span className="border border-dashed border-foreground px-2 py-[3px] text-foreground">
          next dev
        </span>
      </div>
    ),
    copy: "Wrap the dev command you already run. Ocel does the plumbing around it — nothing to rewrite.",
  },
  {
    label: "SECRETS",
    visual: (
      <div className="flex items-center gap-2 font-mono text-[12.5px]">
        <span className="text-dim line-through decoration-dim">.env</span>
        <span className="text-primary">→</span>
        <span className="bg-chip px-2 py-[3px] text-foreground">
          process.env
        </span>
      </div>
    ),
    copy: "Connection strings and keys are injected at boot. No .env stitching between services.",
  },
  {
    label: "NO LOCAL STACK",
    visual: (
      <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5 font-mono text-[12.5px]">
        <span className="text-dim line-through decoration-dim">docker</span>
        <span className="text-dim line-through decoration-dim">emulators</span>
        <span className="inline-flex items-center gap-1.5 text-foreground">
          <span className="size-[7px] rounded-full bg-chart-2" />
          real cloud
        </span>
      </div>
    ),
    copy: "No containers, no emulators to boot. You develop against real infrastructure, instantly.",
  },
  {
    label: "ZERO SETUP",
    visual: (
      <div className="flex items-center gap-2 font-mono text-[12.5px]">
        <span className="inline-flex items-center gap-1.5 border border-dashed border-dim px-2 py-[3px] text-dim">
          <FaAws className="size-3.5" />
          AWS
        </span>
        <span className="text-dim">at deploy →</span>
      </div>
    ),
    copy: "No cloud account to wire up first. Bring your AWS only when you're ready to ship.",
  },
];

function DevMode() {
  return (
    <section className="relative border-t border-border">
      <span className="absolute left-[44%] top-10 font-mono text-[15px] text-primary">
        +
      </span>
      <div className="mx-auto max-w-[1180px] px-10 py-[84px]">
        <div className="grid grid-cols-1 gap-12 md:grid-cols-2">
          <div>
            <Eyebrow>03 — DEV MODE</Eyebrow>
            <SectionHeading>Dev that mirrors production.</SectionHeading>
            <p className="mt-4 max-w-[46ch] text-[15px] leading-[1.65] text-muted-foreground">
              No emulators, no Docker, no shared staging database.{" "}
              <span className="bg-chip px-[5px] py-px font-mono text-[13.5px]">
                ocel dev
              </span>{" "}
              connects you to real cloud resources in seconds — a private
              sandbox for every developer on the team.
            </p>
            <p className="mt-3.5 max-w-[46ch] text-[15px] leading-[1.65] text-muted-foreground">
              When your code reaches production it runs against your own
              account. No code changes. It just works.
            </p>
          </div>
          <Terminal
            title="acme — ocel dev"
            className="self-start"
            bodyClassName="px-5.5 py-5 text-[13.5px] leading-[1.9] text-foreground"
          >
            <div>
              <span className="text-dim">$</span> ocel dev
            </div>
            <div>
              <span className="text-chart-2">●</span> sandbox ready{" "}
              <span className="text-dim">— 4s, real infra</span>
            </div>
            <div className="text-dim">
              &nbsp;&nbsp;postgres main ······· connected
            </div>
            <div className="text-dim">
              &nbsp;&nbsp;bucket uploads ······ connected
            </div>
            <div className="text-dim">
              &nbsp;&nbsp;queue emails ········ connected
            </div>
            <div className="mt-2">
              <span className="text-primary">→</span> watching src/{" "}
              <span className="text-dim">
                — your team gets their own sandboxes too
              </span>
            </div>
          </Terminal>
        </div>
        <div className="mt-[52px] grid grid-cols-1 border-[1.5px] border-foreground md:grid-cols-4">
          {devModeTiles.map((tile, i) => (
            <div
              key={tile.label}
              className={
                i < devModeTiles.length - 1
                  ? "border-b border-border p-6 md:border-b-0 md:border-r"
                  : "p-6"
              }
            >
              <div className="mb-3.5 font-mono text-[11px] tracking-[0.1em] text-dim">
                {tile.label}
              </div>
              <div className="mb-3.5">{tile.visual}</div>
              <p className="text-[13.5px] leading-[1.55] text-muted-foreground">
                {tile.copy}
              </p>
            </div>
          ))}
        </div>
      </div>
    </section>
  );
}

/* ------------------------------------------------------------ 04 · console */

function Console() {
  return (
    <section className="relative overflow-hidden border-t border-border bg-secondary">
      <span className="absolute left-3.5 top-[54%] origin-top-left rotate-90 font-mono text-[10.5px] tracking-[0.14em] text-dim">
        FIG. 02 — CONSOLE
      </span>
      <div className="mx-auto max-w-[1180px] px-10 py-[84px]">
        <div className="mx-auto max-w-[620px] text-center">
          <Eyebrow>04 — CONSOLE</Eyebrow>
          <SectionHeading>A dashboard for infra you still own.</SectionHeading>
          <p className="mx-auto mt-4 max-w-[52ch] text-[15px] leading-[1.65] text-muted-foreground">
            Git integration, environments and variables, team access, logs and
            metrics — everything you'd expect from a platform, connected to your
            production account with scoped access. Nothing hosted in ours.
          </p>
        </div>
        <div className="mt-14 border-[1.5px] border-foreground bg-card shadow-[6px_6px_0_0_var(--hard-shadow)]">
          <Image
            src="/dashboard.png"
            alt="The Ocel console showing a project overview pointed at your own AWS account"
            width={2562}
            height={1730}
            className="h-auto w-full"
            priority={false}
          />
        </div>
      </div>
    </section>
  );
}

/* ------------------------------------------------------------ 05 · interop */

function Interop() {
  return (
    <section className="relative border-t border-border">
      <span className="absolute bottom-9 right-12 font-mono text-[15px] text-faint">
        +
      </span>
      <div className="mx-auto grid max-w-[1180px] grid-cols-1 items-center gap-12 px-10 py-[84px] md:grid-cols-2">
        <div>
          <Eyebrow>05 — INTEROP</Eyebrow>
          <SectionHeading>Bring your own IaC.</SectionHeading>
          <p className="mt-4 max-w-[46ch] text-[15px] leading-[1.65] text-muted-foreground">
            Already on Pulumi or SST? Interop mode hands resource provisioning
            to your tool — configure your database however you want. Ocel
            deploys the app and consumes the outputs. Plugins shape what Ocel
            manages: tag functions, pin them in a VPC, and more.
          </p>
        </div>
        <DarkCodePane file="ocel.json">
          <div>{"{"}</div>
          <div>
            &nbsp;&nbsp;<span className="text-[#7da6ff]">"infra"</span>:{" "}
            <span className="text-[#7da6ff]">"pulumi"</span>,{" "}
            <span className="text-[#5f646b]">{"// your tool provisions"}</span>
          </div>
          <div>
            &nbsp;&nbsp;<span className="text-[#7da6ff]">"plugins"</span>: [
            <span className="text-[#7da6ff]">"vpc"</span>,{" "}
            <span className="text-[#7da6ff]">"tags"</span>]
          </div>
          <div>{"}"}</div>
        </DarkCodePane>
      </div>
    </section>
  );
}

/* ------------------------------------------------------------- 06 · pricing */

function Pricing() {
  return (
    <section className="border-t border-border bg-secondary">
      <div className="mx-auto grid max-w-[1180px] grid-cols-1 items-center gap-12 px-10 py-[84px] md:grid-cols-2">
        <div>
          <Eyebrow>06 — THE MATH</Eyebrow>
          <SectionHeading>Read the bill, not the brochure.</SectionHeading>
          <p className="mt-4 max-w-[46ch] text-[15px] leading-[1.65] text-muted-foreground">
            Seat-priced platforms charge for the privilege of marking up compute
            you already pay a cloud for. Ocel runs in your account — the only
            bill is the one you already have.
          </p>
        </div>
        <div className="border-[1.5px] border-foreground bg-card font-mono text-sm text-foreground shadow-[6px_6px_0_0_var(--hard-shadow)]">
          <div className="flex justify-between border-b border-border px-5 py-4">
            <span className="text-dim">20 seats × $20/mo, elsewhere</span>
            <span>$400/mo</span>
          </div>
          <div className="flex justify-between border-b border-border px-5 py-4">
            <span className="text-dim">markup on your compute</span>
            <span>10–1000%</span>
          </div>
          <div className="flex justify-between bg-primary px-5 py-4 text-primary-foreground">
            <span>ocel markup, forever</span>
            <span className="font-semibold">$0</span>
          </div>
        </div>
      </div>
    </section>
  );
}

/* ---------------------------------------------------------------- 07 · faq */

const faqs = [
  {
    q: "Is this a PaaS?",
    a: "It's the PaaS experience — deploys, previews, logs, rollbacks — but nothing runs in our account. You connect your own cloud with scoped, revocable access.",
  },
  {
    q: "Do I have to use the SDK?",
    a: "No. The CLI alone gives you zero-config deploys. The SDK is there when your app needs a database, storage, or queues.",
  },
  {
    q: "We already use Pulumi / SST.",
    a: "Keep it. Interop mode hands provisioning to your tool; Ocel deploys the app and consumes the outputs.",
  },
  {
    q: "Where does dev mode run?",
    a: 'Sandboxes run on Ocel\'s dev cloud so your team is productive in seconds — the one exception to "your account". Production always runs in yours.',
  },
];

function Faq() {
  return (
    <section className="border-t border-border">
      <Container>
        <Eyebrow>07 — FAQ</Eyebrow>
        <h2 className="mb-[34px] text-[34px] font-semibold leading-[1.15] tracking-[-0.02em] text-foreground">
          The questions engineers ask.
        </h2>
        <div className="grid grid-cols-1 border-[1.5px] border-foreground md:grid-cols-2">
          {faqs.map((f, i) => (
            <div
              key={f.q}
              className={
                "border-border p-6" +
                (i % 2 === 0 ? " md:border-r" : "") +
                (i < 2 ? " border-b max-md:border-b" : " max-md:border-t")
              }
            >
              <div className="mb-2 text-[15px] font-semibold text-foreground">
                {f.q}
              </div>
              <div className="text-sm leading-[1.6] text-muted-foreground">
                {f.a}
              </div>
            </div>
          ))}
        </div>
      </Container>
    </section>
  );
}

/* ------------------------------------------------------------- cta + footer */

function CtaBand() {
  return (
    <section className="relative overflow-hidden bg-foreground px-10 py-20 text-center">
      <span className="absolute left-[60px] top-10 font-mono text-[15px] text-dim">
        +
      </span>
      <span className="absolute bottom-11 right-20 font-mono text-[15px] text-primary">
        +
      </span>
      <h2 className="text-[40px] font-semibold leading-[1.1] tracking-[-0.03em] text-background">
        Deploy to your own cloud in minutes.
      </h2>
      <div className="mt-6 flex justify-center gap-3">
        <Button className="h-auto rounded-none px-6 py-[13px] text-[15px] font-semibold normal-case tracking-normal">
          Get started
        </Button>
        <span className="border-[1.5px] border-dim px-[18px] py-3 font-mono text-sm text-background">
          npm i -g ocel
        </span>
      </div>
    </section>
  );
}

const footerCols = [
  { title: "PRODUCT", links: ["CLI", "SDK", "Dev mode", "Console"] },
  { title: "RESOURCES", links: ["Docs", "Changelog", "Blog", "Philosophy"] },
  {
    title: "OPEN SOURCE",
    links: ["GitHub", "Discord", "Contributing", "License (MIT)"],
  },
];

function OneMoreThing() {
  return (
    <section
      className="relative overflow-hidden px-10 pb-[92px] pt-20 text-center"
      style={{
        backgroundImage:
          "linear-gradient(var(--grid) 1px,transparent 1px),linear-gradient(90deg,var(--grid) 1px,transparent 1px)",
        backgroundSize: "48px 48px",
      }}
    >
      <div
        className="absolute inset-0"
        style={{
          background:
            "linear-gradient(to bottom,var(--background) 0%,transparent 20%),radial-gradient(ellipse 70% 85% at 50% 45%,transparent 22%,var(--background) 78%)",
        }}
      />
      <div className="relative">
        <div className="mb-[22px] font-mono text-xs tracking-[0.14em] text-dim">
          — ONE MORE THING —
        </div>
        <h2 className="text-[54px] font-semibold leading-[1.08] tracking-[-0.03em] text-foreground">
          Oh — and it's <span className="text-primary">open source.</span>
        </h2>
        <p className="mx-auto mt-[18px] max-w-[52ch] text-[15px] leading-[1.65] text-muted-foreground">
          MIT licensed. Nothing runs in our account. If you stopped using Ocel
          tomorrow, the infrastructure and the code describing it are still
          yours to read and run.
        </p>
        <div className="mt-6 flex justify-center gap-3">
          <Button className="h-auto rounded-none bg-foreground px-[22px] py-3 text-sm font-semibold normal-case tracking-normal text-background hover:bg-foreground/85">
            ★ Star on GitHub
          </Button>
          <span className="border-[1.5px] border-foreground px-4 py-[11px] font-mono text-[13px] text-foreground">
            read the source →
          </span>
        </div>
      </div>
    </section>
  );
}

function Footer() {
  return (
    <section className="border-t border-border px-10 pb-[26px] pt-[54px] text-center">
      <div className="mx-auto grid max-w-[1180px] grid-cols-1 gap-8 text-left md:grid-cols-[1.4fr_1fr_1fr_1fr]">
        <div>
          <Wordmark markSize={18} textSize={15} />
          <div className="mt-2.5 max-w-[26ch] text-[12.5px] leading-[1.6] text-dim">
            Deploy apps to your own cloud.
          </div>
        </div>
        {footerCols.map((col) => (
          <div
            key={col.title}
            className="text-[13px] leading-[2.1] text-muted-foreground"
          >
            <div className="mb-1.5 font-mono text-[11px] tracking-[0.1em] text-dim">
              {col.title}
            </div>
            {col.links.map((link) => (
              <div key={link}>{link}</div>
            ))}
          </div>
        ))}
      </div>
      <div className="mx-auto mt-[34px] flex max-w-[1180px] justify-between border-t border-border pt-4 font-mono text-[11.5px] text-dim">
        <span>© 2026 ocel</span>
        <span>FIG. 04 — YOUR CLOUD. YOUR CODE. YOUR CALL.</span>
      </div>
    </section>
  );
}

import type { ComponentType, ReactNode } from "react";
import {
  AwsLogo,
  FlyLogo,
  KubernetesLogo,
  RailwayLogo,
  TerraformLogo,
  VercelLogo,
} from "@/components/landing/tool-logos";

type Tool = {
  name: string;
  Logo: ComponentType<{ className?: string }>;
  size: string;
};

const managed: Tool[] = [
  { name: "Vercel", Logo: VercelLogo, size: "h-[13px]" },
  { name: "Railway", Logo: RailwayLogo, size: "h-[15px]" },
  { name: "Fly.io", Logo: FlyLogo, size: "h-[16px]" },
];

const diy: Tool[] = [
  { name: "AWS", Logo: AwsLogo, size: "h-[11px]" },
  { name: "Kubernetes", Logo: KubernetesLogo, size: "h-[15px]" },
  { name: "Terraform", Logo: TerraformLogo, size: "h-[16px]" },
];

function ToolRow({ tools }: { tools: Tool[] }) {
  return (
    <div className="mt-6 flex flex-wrap gap-2">
      {tools.map(({ name, Logo, size }) => (
        <span
          key={name}
          className="inline-flex items-center gap-2 border border-border px-2.5 py-1.5 transition-colors hover:border-foreground"
        >
          <span className="flex w-5 justify-center text-foreground">
            <Logo className={`${size} w-auto`} />
          </span>
          <span className="font-mono text-[12.5px] text-muted-foreground">
            {name}
          </span>
        </span>
      ))}
    </div>
  );
}

function Trade({ good, bad }: { good: string; bad: ReactNode }) {
  return (
    <div className="mt-5 space-y-2.5">
      <div className="flex gap-3">
        <span className="mt-px font-mono text-[13px] text-chart-2">✓</span>
        <p className="text-[15px] font-medium leading-[1.55] text-foreground">
          {good}
        </p>
      </div>
      <div className="flex gap-3">
        <span className="mt-px font-mono text-[13px] text-primary">✕</span>
        <p className="text-[15px] leading-[1.55] text-muted-foreground">
          {bad}
        </p>
      </div>
    </div>
  );
}

function Panel({
  label,
  question,
  children,
  tools,
  className,
}: {
  label: string;
  question: string;
  children: ReactNode;
  tools: Tool[];
  className?: string;
}) {
  return (
    <div className={`p-7 md:p-9 ${className ?? ""}`}>
      <div className="font-mono text-[11px] tracking-[0.1em] text-dim">
        {label}
      </div>
      <h3 className="mt-3 text-[21px] font-semibold leading-[1.2] tracking-[-0.01em] text-foreground">
        {question}
      </h3>
      {children}
      <ToolRow tools={tools} />
    </div>
  );
}

export function StatusQuo() {
  return (
    <section className="relative overflow-hidden border-t-[1.5px] border-foreground">
      <span className="absolute right-10 top-11 font-mono text-[15px] text-faint">
        +
      </span>
      <span className="absolute bottom-12 left-10 font-mono text-[15px] text-primary">
        +
      </span>
      <div className="mx-auto max-w-[1180px] px-10 py-[84px]">
        <div className="mb-3.5 font-mono text-xs tracking-[0.08em] text-primary">
          THE STATUS QUO
        </div>
        <h2 className="text-[34px] font-semibold leading-[1.15] tracking-[-0.02em] text-foreground">
          Today, you have to choose.
        </h2>
        <p className="mt-3.5 max-w-[52ch] text-[15px] leading-[1.65] text-muted-foreground">
          Every tool sits on one side of a line. Pick the experience or pick the
          ownership — never both.
        </p>

        <div className="relative mt-9 grid grid-cols-1 border-[1.5px] border-foreground md:grid-cols-2">
          <Panel
            label="MANAGED PLATFORMS"
            question="Want great developer experience?"
            tools={managed}
          >
            <Trade
              good="Ship in minutes."
              bad="But on someone else's platform, with someone else's limits."
            />
          </Panel>

          <Panel
            label="ROLL YOUR OWN"
            question="Want to own your infrastructure?"
            tools={diy}
            className="border-t border-border md:border-l md:border-t-0"
          >
            <Trade
              good="Full control, no lock-in."
              bad="But weeks of YAML, IAM, and glue code first."
            />
          </Panel>

          <span className="absolute left-1/2 top-1/2 hidden size-11 -translate-x-1/2 -translate-y-1/2 items-center justify-center border-[1.5px] border-foreground bg-background font-mono text-[12px] tracking-[0.08em] text-dim md:flex">
            OR
          </span>
        </div>

        <div className="mt-14 text-center">
          <p className="text-[38px] font-semibold leading-[1.1] tracking-[-0.03em] text-foreground md:text-[50px]">
            You shouldn't have to{" "}
            <span className="relative inline-block">
              choose
              <span
                aria-hidden="true"
                className="pointer-events-none absolute inset-x-0 top-1/2 h-[0.12em] translate-y-[1.5px] rounded-full bg-primary"
              />
            </span>
            .
          </p>
          <div className="mt-9 flex justify-center">
            <svg
              viewBox="0 0 24 24"
              className="h-10 w-10 animate-bob text-primary"
              fill="none"
              stroke="currentColor"
              strokeWidth={2.75}
              strokeLinecap="round"
              strokeLinejoin="round"
              aria-hidden="true"
            >
              <path d="M12 4v13" />
              <path d="m5 11 7 7 7-7" />
            </svg>
          </div>
        </div>
      </div>
    </section>
  );
}

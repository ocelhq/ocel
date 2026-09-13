"use client";

import type { DeploymentApp, DeploymentTrigger } from "@console/db/schema";
import {
  GitBranchIcon,
  GitPullRequestIcon,
  GlobeSimpleIcon,
  RobotIcon,
  TerminalIcon,
} from "@phosphor-icons/react";
import { Avatar, AvatarFallback } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { absoluteTime } from "@/lib/relative-time";
import { initials, type Tone } from "@/lib/runs";
import { AppMark } from "../../../marks";

const tones: Record<Tone, string> = {
  go: "bg-go",
  faint: "bg-faint",
  destructive: "bg-destructive",
};

export function StatusDot({ tone }: { tone: Tone }) {
  return <span aria-hidden className={`size-2 shrink-0 self-center rounded-full ${tones[tone]}`} />;
}

export function EnvironmentBadge({
  environmentClass,
  active,
}: {
  environmentClass: "production" | "preview";
  active: boolean;
}) {
  const production = environmentClass === "production";
  const Icon = production ? GlobeSimpleIcon : GitPullRequestIcon;
  return (
    <Badge
      variant={production && active ? "default" : "outline"}
      title={production && active ? "Currently live" : undefined}
      className="h-6 gap-1.5 px-2 text-sm font-normal [&>svg]:size-3.5!"
    >
      <Icon data-icon="inline-start" />
      {production ? "Production" : "Preview"}
    </Badge>
  );
}

export function AppMarks({ apps, limit = 4 }: { apps: DeploymentApp[]; limit?: number }) {
  if (apps.length === 0) {
    return <span className="text-muted-foreground">—</span>;
  }
  const shown = apps.slice(0, limit);
  const rest = apps.length - shown.length;
  return (
    <span className="inline-flex items-center gap-1">
      {shown.map((app) => (
        <Tooltip key={app.name}>
          <TooltipTrigger
            render={
              <span
                role="img"
                aria-label={app.outcome === "failed" ? `${app.name}, failed` : app.name}
                className="relative grid size-5 place-items-center"
              />
            }
          >
            <AppMark app={app} size={16} />
            {app.outcome === "failed" && (
              <span
                aria-hidden
                className="absolute -right-0.5 -bottom-0.5 size-2 rounded-full bg-destructive ring-2 ring-background"
              />
            )}
          </TooltipTrigger>
          <TooltipContent>
            {app.name}
            {app.outcome === "failed" ? " · failed" : ""}
          </TooltipContent>
        </Tooltip>
      ))}
      {rest > 0 && <span className="text-muted-foreground">+{rest}</span>}
    </span>
  );
}

export function Author({ name }: { name: string | null }) {
  if (!name) {
    return <span className="text-muted-foreground">—</span>;
  }
  return (
    <span className="inline-flex min-w-0 items-center gap-1.5 text-foreground">
      <Avatar className="size-5">
        <AvatarFallback className="bg-muted text-[11px] text-muted-foreground">
          {initials(name)}
        </AvatarFallback>
      </Avatar>
      <span className="truncate">{name}</span>
    </span>
  );
}

const triggerIcons: Record<DeploymentTrigger["kind"], typeof TerminalIcon> = {
  cli: TerminalIcon,
  ci: RobotIcon,
  git: GitBranchIcon,
};

export function Trigger({ trigger, command }: { trigger: DeploymentTrigger; command: string }) {
  const Icon = triggerIcons[trigger.kind];
  const via = trigger.kind === "ci" ? (trigger.ci?.provider ?? "ci") : null;
  return (
    <span className="inline-flex items-center gap-1.5">
      <Icon aria-hidden className="size-3.5 shrink-0 text-muted-foreground" />
      <span>{command}</span>
      {via && <span className="text-muted-foreground">via {via}</span>}
    </span>
  );
}

export function AuthorMark({
  name,
  trigger,
  deployedAt,
}: {
  name: string;
  trigger: DeploymentTrigger;
  deployedAt: string;
}) {
  const via =
    trigger.kind === "ci"
      ? `via ${trigger.ci?.provider ?? "ci"}`
      : trigger.kind === "git"
        ? "via git push"
        : "from the terminal";
  return (
    <Tooltip>
      <TooltipTrigger
        render={<span role="img" aria-label={`${name}, ${via}`} className="inline-flex" />}
      >
        <Avatar className="size-5">
          <AvatarFallback className="bg-muted text-[11px] text-muted-foreground">
            {initials(name)}
          </AvatarFallback>
        </Avatar>
      </TooltipTrigger>
      <TooltipContent className="flex flex-col gap-0.5">
        <span className="font-medium">{name}</span>
        <span className="text-background/70">
          {via} · {absoluteTime(deployedAt)}
        </span>
      </TooltipContent>
    </Tooltip>
  );
}

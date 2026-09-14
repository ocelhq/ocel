import { db } from "@console/db";
import { project } from "@console/db/schema";
import {
  ArrowLeftIcon,
  CaretRightIcon,
  GitBranchIcon,
  GitCommitIcon,
  GitPullRequestIcon,
  GlobeSimpleIcon,
  HeartbeatIcon,
  TerminalWindowIcon,
} from "@phosphor-icons/react/dist/ssr";
import { and, eq } from "drizzle-orm";
import Link from "next/link";
import { notFound } from "next/navigation";
import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from "@/components/ui/accordion";
import { requireOrganization } from "@/lib/access";
import { findRun } from "@/lib/deployments";
import { absoluteTime } from "@/lib/relative-time";
import {
  authorOf,
  commandOf,
  duration,
  kindVerbs,
  promotes,
  runHref,
  runStatus,
  shortId,
} from "@/lib/runs";
import { labelType } from "@/lib/type";
import { EdgeMark, ProviderMark } from "../../../../marks";
import { Stamp } from "../../../../stamp";
import { Author, EnvironmentBadge, StatusDot, Trigger } from "../cells";
import { RunActions } from "./actions";
import {
  AppList,
  BuildLog,
  Checks,
  checksOf,
  Domains,
  Field,
  hostnamesOf,
  Joined,
  NotReported,
  Panel,
  ResourceList,
  readersOf,
} from "./sections";
import { LoadError, NotFound } from "./states";

const trigger =
  "h-12 items-center justify-start gap-2.5 px-4 text-sm font-medium hover:bg-muted/50 hover:no-underline **:data-[slot=accordion-trigger-icon]:hidden";

const icon = "size-4 text-muted-foreground";

const aside = "font-normal text-muted-foreground";

function Caret() {
  return (
    <CaretRightIcon
      aria-hidden
      className="size-3.5 text-muted-foreground transition-transform group-aria-expanded/accordion-trigger:rotate-90"
    />
  );
}

const panel = "border-t border-border p-0";

function countOf(n: number, noun: string) {
  return n === 0 ? "None" : `${n} ${noun}${n === 1 ? "" : "s"}`;
}

export default async function RunPage({
  params,
}: {
  params: Promise<{ slug: string; id: string }>;
}) {
  const [{ slug, id }, session] = await Promise.all([params, requireOrganization()]);

  const [found] = await db
    .select({ id: project.id })
    .from(project)
    .where(and(eq(project.organizationId, session.activeOrganizationId), eq(project.slug, slug)));
  if (!found) {
    notFound();
  }

  const load = await findRun(found.id, id);
  const now = new Date().toISOString();
  const back = `/projects/${slug}/deployments`;

  if (load.error) {
    return <LoadError href={runHref(slug, id)} back={back} />;
  }
  if (!load.run) {
    return <NotFound back={back} />;
  }

  const { run, active } = load;
  const status = runStatus(run);
  const isActive = active?.id === run.id;
  const superseded = !isActive && promotes(run.kind) && run.outcome === "succeeded" && active;
  const took = duration(run);
  const url = run.topology.apps.flatMap((app) => app.urls)[0] ?? null;
  const title =
    shortId(run.promotionId) ??
    (run.outcome === "failed" ? `Failed ${kindVerbs[run.kind]}` : status.word);
  const stageCount = run.trace.length;
  const EnvironmentIcon =
    run.environmentClass === "production" ? GlobeSimpleIcon : GitPullRequestIcon;
  const hostnames = hostnamesOf(run.topology.apps).length;
  const checks = checksOf(run.topology.apps).length;

  return (
    <div className="flex flex-1 flex-col gap-6 px-5 pt-8 pb-12 md:px-8">
      <header className="flex flex-col gap-4">
        <Link
          href={back}
          className={`${labelType} inline-flex w-fit items-center gap-1.5 underline-offset-4 outline-none hover:underline focus-visible:underline`}
        >
          <ArrowLeftIcon aria-hidden className="size-3.5" />
          Deployments
        </Link>
        <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-4">
          <div className="flex flex-col gap-3">
            <h1
              title={run.promotionId ?? undefined}
              className={`text-2xl font-semibold tracking-tight ${run.promotionId ? "font-mono" : ""}`}
            >
              {title}
            </h1>
            <div className="flex flex-wrap items-center gap-x-3 gap-y-2 text-sm">
              <span className="inline-flex items-center gap-2">
                <StatusDot tone={status.tone} />
                <span className={status.tone === "destructive" ? "text-destructive" : ""}>
                  {status.word}
                </span>
              </span>
              <span className="text-muted-foreground">
                <Stamp at={run.deployedAt.toISOString()} now={now} />
              </span>
              <EnvironmentBadge environmentClass={run.environmentClass} active={isActive} />
              {run.environmentIdentity && (
                <span className="text-muted-foreground">{run.environmentIdentity}</span>
              )}
              {run.tag && <span className="text-muted-foreground">{run.tag}</span>}
            </div>
            {superseded && active.promotionId && (
              <p className="text-sm text-muted-foreground">
                Superseded by{" "}
                <Link
                  href={runHref(slug, active.id)}
                  className="font-mono text-[13px] text-foreground underline-offset-4 outline-none hover:underline focus-visible:underline"
                >
                  {shortId(active.promotionId)}
                </Link>{" "}
                <Stamp at={active.deployedAt.toISOString()} now={now} />.
              </p>
            )}
            {run.error && (
              <p
                role="alert"
                className="max-w-xl border border-destructive p-3 font-mono text-xs text-destructive"
              >
                {run.error}
              </p>
            )}
          </div>
          <RunActions
            url={url}
            kind={run.kind}
            outcome={run.outcome}
            promotionId={run.promotionId}
            environmentClass={run.environmentClass}
            environmentIdentity={run.environmentIdentity}
            active={isActive}
          />
        </div>
      </header>

      <Joined className="sm:grid-cols-2 lg:grid-cols-3">
        <Field name="Environment">
          <span className="inline-flex items-center gap-1.5">
            <EnvironmentIcon aria-hidden className="size-4 text-muted-foreground" />
            {run.environmentClass}
            {run.environmentIdentity ? ` · ${run.environmentIdentity}` : ""}
          </span>
        </Field>
        <Field name="Provider">
          <span className="inline-flex items-center gap-2">
            <ProviderMark provider={run.providerName} size={18} />
            {[run.providerName, run.providerRegion].filter(Boolean).join(" · ")}
          </span>
        </Field>
        <Field name="Edge">
          {run.edgeKind ? (
            <span className="inline-flex items-center gap-2">
              <EdgeMark edge={run.edgeKind} size={16} />
              {run.edgeKind}
            </span>
          ) : (
            <NotReported />
          )}
        </Field>
        <Field name="Author">
          <Author name={authorOf(run.trigger)} />
        </Field>
        <Field name="Trigger">
          <Trigger trigger={run.trigger} command={commandOf(run)} />
        </Field>
        <Field name="Source">
          {run.git ? (
            <span className="flex flex-col gap-1">
              <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
                {run.git.branch && (
                  <span className="inline-flex items-center gap-1.5">
                    <GitBranchIcon aria-hidden className="size-4 text-muted-foreground" />
                    {run.git.branch}
                  </span>
                )}
                <span className="inline-flex items-center gap-1.5">
                  <GitCommitIcon aria-hidden className="size-4 text-muted-foreground" />
                  <span className="font-mono text-xs">{run.git.sha.slice(0, 7)}</span>
                </span>
                {run.git.dirty && (
                  <span className="text-xs text-muted-foreground">uncommitted changes</span>
                )}
              </span>
              {run.git.message && (
                <span className="line-clamp-2 text-muted-foreground">{run.git.message}</span>
              )}
            </span>
          ) : (
            <NotReported />
          )}
        </Field>
        <Field name="Started">
          {run.startedAt ? absoluteTime(run.startedAt) : <NotReported />}
        </Field>
        <Field name="Duration">{took ?? <NotReported />}</Field>
        <Field name="CLI">
          <span className="flex flex-col gap-0.5">
            <span>{run.cliVersion ? `ocel ${run.cliVersion}` : <NotReported />}</span>
            <span className="font-mono text-xs text-muted-foreground">run {run.runId}</span>
          </span>
        </Field>
      </Joined>

      <Joined>
        <Panel heading="Apps">
          <AppList apps={run.topology.apps} />
        </Panel>
        <Panel heading="Resources">
          <ResourceList
            resources={run.topology.resources}
            readers={readersOf(run)}
            provider={run.providerName}
          />
        </Panel>
      </Joined>

      <Accordion className="border border-border">
        <AccordionItem value="build">
          <AccordionTrigger className={trigger}>
            <Caret />
            <TerminalWindowIcon aria-hidden className={icon} />
            Build logs
            <span className={aside}>
              {stageCount === 0 ? "Not reported" : (took ?? `${stageCount} stages`)}
            </span>
          </AccordionTrigger>
          <AccordionContent className={panel}>
            <BuildLog stages={run.trace} />
          </AccordionContent>
        </AccordionItem>
        <AccordionItem value="domains">
          <AccordionTrigger className={trigger}>
            <Caret />
            <GlobeSimpleIcon aria-hidden className={icon} />
            Domains
            <span className={aside}>{countOf(hostnames, "hostname")}</span>
          </AccordionTrigger>
          <AccordionContent className={panel}>
            <Domains apps={run.topology.apps} />
          </AccordionContent>
        </AccordionItem>
        <AccordionItem value="checks">
          <AccordionTrigger className={trigger}>
            <Caret />
            <HeartbeatIcon aria-hidden className={icon} />
            Checks
            <span className={aside}>{countOf(checks, "check")}</span>
          </AccordionTrigger>
          <AccordionContent className={panel}>
            <Checks apps={run.topology.apps} />
          </AccordionContent>
        </AccordionItem>
      </Accordion>
    </div>
  );
}

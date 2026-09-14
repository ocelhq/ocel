"use client";

import type { DeploymentApp, DeploymentResource, DeploymentTopology } from "@console/db/schema";
import { ArrowSquareOutIcon, XIcon } from "@phosphor-icons/react";
import { useEffect, useRef } from "react";
import { Sheet, SheetContent } from "@/components/ui/sheet";
import { useIsMobile } from "@/hooks/use-mobile";
import { labelType } from "@/lib/type";
import { AppMark, OutcomeDot, ProviderMark, ResourceMark } from "../../../marks";

export type Selection = { kind: "app"; name: string } | { kind: "resource"; name: string };

function Section({ heading, children }: { heading: string; children: React.ReactNode }) {
  return (
    <section className="flex flex-col gap-2 border-t border-border px-5 py-4">
      <h3 className={labelType}>{heading}</h3>
      {children}
    </section>
  );
}

function Field({ name, value }: { name: string; value: string }) {
  return (
    <div className="flex items-baseline justify-between gap-4 text-xs">
      <span className="text-muted-foreground">{name}</span>
      <span className="truncate font-mono text-foreground">{value}</span>
    </div>
  );
}

function Files({ files }: { files: string[] }) {
  return (
    <ul className="flex flex-col gap-0.5">
      {files.map((file) => (
        <li key={file} className="truncate font-mono text-xs text-muted-foreground">
          {file}
        </li>
      ))}
    </ul>
  );
}

function AppDetails({ app, topology }: { app: DeploymentApp; topology: DeploymentTopology }) {
  const reads = topology.usages.filter((usage) => usage.app === app.name);

  return (
    <>
      {app.error && (
        <p className="border-t border-border px-5 py-4 font-mono text-xs text-destructive">
          {app.error}
        </p>
      )}
      <Section heading="Urls">
        {app.urls.length === 0 ? (
          <p className="text-xs text-muted-foreground">No url was reported for this app.</p>
        ) : (
          <ul className="flex flex-col gap-1">
            {app.urls.map((url) => (
              <li key={url} className="min-w-0">
                <a
                  href={url}
                  target="_blank"
                  rel="noreferrer"
                  className="flex min-w-0 items-center gap-1 font-mono text-xs underline-offset-4 hover:underline"
                >
                  <span className="truncate">{url}</span>
                  <ArrowSquareOutIcon aria-hidden className="size-3 shrink-0" />
                </a>
              </li>
            ))}
          </ul>
        )}
      </Section>
      <Section heading="Runtime">
        <div className="flex flex-col gap-1.5">
          <Field name="Runtime" value={app.runtime.name} />
          {app.runtime.arch && <Field name="Architecture" value={app.runtime.arch} />}
          <Field name="Compute" value={app.compute} />
          {app.folder && <Field name="Folder" value={app.folder} />}
          {app.deploymentId && <Field name="Deployment" value={app.deploymentId.slice(0, 12)} />}
        </div>
      </Section>
      <Section heading="Variables">
        {app.variables.length === 0 ? (
          <p className="text-xs text-muted-foreground">This app reported no variables.</p>
        ) : (
          <table className="w-full border-collapse text-xs">
            <thead>
              <tr>
                <th className={`${labelType} w-full py-1 text-left font-medium`}>Key</th>
                <th className={`${labelType} w-px py-1 text-left font-medium`}>Class</th>
                <th className={`${labelType} hidden w-px py-1 text-left font-medium md:table-cell`}>
                  Folder
                </th>
              </tr>
            </thead>
            <tbody>
              {app.variables.map((variable) => (
                <tr key={variable.key} className="border-t border-border align-baseline">
                  <td className="w-full min-w-0 break-all py-1.5 pr-3 font-mono">{variable.key}</td>
                  <td className="w-px whitespace-nowrap py-1.5">
                    <span
                      className={`${labelType} border border-border bg-background px-1.5 py-0.5`}
                    >
                      {variable.class}
                    </span>
                  </td>
                  <td className="hidden w-px whitespace-nowrap py-1.5 pl-3 font-mono text-muted-foreground md:table-cell">
                    {variable.folder}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Section>
      <Section heading="Reads">
        {reads.length === 0 ? (
          <p className="text-xs text-muted-foreground">This app reads no resources.</p>
        ) : (
          <ul className="flex flex-col gap-3">
            {reads.map((usage) => (
              <li key={usage.resource} className="flex flex-col gap-1">
                <span className="text-xs font-semibold">{usage.resource}</span>
                <Files files={usage.files} />
              </li>
            ))}
          </ul>
        )}
      </Section>
    </>
  );
}

function ResourceDetails({
  resource,
  topology,
}: {
  resource: DeploymentResource;
  topology: DeploymentTopology;
}) {
  const readers = topology.usages.filter((usage) => usage.resource === resource.name);

  return (
    <>
      <Section heading="Binding">
        <div className="flex flex-col gap-1.5">
          <Field name="Name" value={resource.binding.name} />
          {resource.binding.source && <Field name="Source" value={resource.binding.source} />}
          <Field name="Type" value={resource.type} />
        </div>
      </Section>
      <Section heading="Keys">
        {resource.binding.propertyKeys.length === 0 ? (
          <p className="text-xs text-muted-foreground">This binding exposes no keys.</p>
        ) : (
          <ul className="flex flex-wrap gap-1.5">
            {resource.binding.propertyKeys.map((key) => (
              <li
                key={key}
                className={`${labelType} border border-border bg-background px-1.5 py-0.5`}
              >
                {key}
              </li>
            ))}
          </ul>
        )}
      </Section>
      <Section heading="Grants">
        {resource.binding.grants.length === 0 ? (
          <p className="text-xs text-muted-foreground">This binding grants nothing.</p>
        ) : (
          <ul className="flex flex-col gap-2">
            {resource.binding.grants.map((grant) => (
              <li key={grant.verb ?? grant.actions.join()} className="flex flex-col gap-1">
                {grant.verb && <span className="text-xs font-semibold">{grant.verb}</span>}
                <span className="font-mono text-xs text-muted-foreground">
                  {grant.actions.join(", ")}
                </span>
              </li>
            ))}
          </ul>
        )}
      </Section>
      <Section heading="Read by">
        {readers.length === 0 ? (
          <p className="text-xs text-muted-foreground">No app reads this resource.</p>
        ) : (
          <ul className="flex flex-col gap-3">
            {readers.map((usage) => (
              <li key={usage.app} className="flex flex-col gap-1">
                <span className="text-xs font-semibold">{usage.app}</span>
                <Files files={usage.files} />
              </li>
            ))}
          </ul>
        )}
      </Section>
    </>
  );
}

function Body({
  selection,
  topology,
  provider,
  onClose,
}: {
  selection: Selection;
  topology: DeploymentTopology;
  provider: string;
  onClose: () => void;
}) {
  const app =
    selection.kind === "app"
      ? topology.apps.find((item) => item.name === selection.name)
      : undefined;
  const resource =
    selection.kind === "resource"
      ? topology.resources.find((item) => item.name === selection.name)
      : undefined;

  return (
    <>
      <header className="flex items-center gap-2 px-5 py-4">
        {app && <AppMark app={app} />}
        {resource && <ResourceMark resource={resource} />}
        <h2 className="min-w-0 flex-1 truncate text-sm font-semibold">{selection.name}</h2>
        {resource && <ProviderMark provider={provider} resourceType={resource.type} />}
        {app && <OutcomeDot outcome={app.outcome} />}
        <button
          type="button"
          onClick={onClose}
          aria-label="Close details"
          className="grid size-8 place-items-center text-muted-foreground outline-none transition-colors hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/40"
        >
          <XIcon aria-hidden className="size-4" />
        </button>
      </header>
      {app && <AppDetails app={app} topology={topology} />}
      {resource && <ResourceDetails resource={resource} topology={topology} />}
    </>
  );
}

export function DetailsPanel({
  selection,
  topology,
  provider,
  onClose,
}: {
  selection: Selection;
  topology: DeploymentTopology;
  provider: string;
  onClose: () => void;
}) {
  const panel = useRef<HTMLElement>(null);
  const mobile = useIsMobile();

  useEffect(() => {
    panel.current?.focus();
  }, []);

  if (mobile) {
    return (
      <Sheet open onOpenChange={(open) => !open && onClose()}>
        <SheetContent
          side="bottom"
          showCloseButton={false}
          aria-label={`${selection.name} details`}
          className="max-h-[70svh] overflow-y-auto"
        >
          <Body selection={selection} topology={topology} provider={provider} onClose={onClose} />
        </SheetContent>
      </Sheet>
    );
  }

  return (
    <aside
      ref={panel}
      tabIndex={-1}
      aria-label={`${selection.name} details`}
      onKeyDown={(event) => {
        if (event.key === "Escape") {
          onClose();
        }
      }}
      className="flex w-90 shrink-0 flex-col overflow-y-auto border-l border-border bg-background outline-none"
    >
      <Body selection={selection} topology={topology} provider={provider} onClose={onClose} />
    </aside>
  );
}

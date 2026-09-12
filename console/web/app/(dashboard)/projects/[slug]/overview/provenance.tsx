"use client";

import {
  ArrowsOutIcon,
  MagnifyingGlassMinusIcon,
  MagnifyingGlassPlusIcon,
} from "@phosphor-icons/react";
import { useReactFlow } from "@xyflow/react";
import { useEffect, useRef, useState } from "react";
import { EnvironmentSwitcher } from "../../../environment-switcher";
import { labelType } from "../../../label";
import { Stamp } from "../../../stamp";
import { useFitOptions } from "./nodes";

export type Provenance = {
  deployedAt: string;
  promotionId: string | null;
  tag: string | null;
  providerName: string;
  providerRegion: string | null;
};

export type Failure = { deployedAt: string; error: string | null };

function FailureBanner({ failure, now }: { failure: Failure; now: string }) {
  const [open, setOpen] = useState(false);
  const [clamped, setClamped] = useState(false);
  const text = useRef<HTMLParagraphElement>(null);
  const error = failure.error;

  useEffect(() => {
    if (open || !error) {
      return;
    }
    const element = text.current;
    setClamped(element ? element.scrollHeight > element.clientHeight : false);
  }, [open, error]);

  return (
    <div
      className="max-w-xl border border-destructive bg-background p-3 text-destructive"
      role="alert"
    >
      <p className={`${labelType} text-destructive`}>
        <Stamp at={failure.deployedAt} now={now} prefix="Deploy failed" />
      </p>
      {error && (
        <>
          <p ref={text} className={`mt-1.5 font-mono text-xs ${open ? "" : "line-clamp-2"}`}>
            {error}
          </p>
          {clamped && (
            <button
              type="button"
              onClick={() => setOpen(!open)}
              className={`${labelType} mt-1.5 text-destructive underline underline-offset-4 outline-none focus-visible:ring-2 focus-visible:ring-ring/40`}
            >
              {open ? "Less" : "More"}
            </button>
          )}
        </>
      )}
    </div>
  );
}

export function ProvenanceStrip({
  provenance,
  failure,
  now,
  prefix = "As at",
}: {
  provenance?: Provenance;
  failure?: Failure;
  now: string;
  prefix?: string;
}) {
  const parts = provenance
    ? [
        provenance.promotionId ? `promotion ${provenance.promotionId.slice(0, 7)}` : null,
        provenance.tag,
        provenance.providerName,
        provenance.providerRegion,
      ].filter((part): part is string => Boolean(part))
    : [];

  return (
    <div className="pointer-events-none absolute top-4 left-4 z-10 flex max-w-[calc(100%-2rem)] flex-col items-start gap-2">
      <div className="pointer-events-auto border border-border bg-background">
        <EnvironmentSwitcher />
      </div>
      {failure && (
        <div className="pointer-events-auto">
          <FailureBanner failure={failure} now={now} />
        </div>
      )}
      {provenance && (
        <p className={`pointer-events-auto bg-background ${labelType}`}>
          <Stamp at={provenance.deployedAt} now={now} prefix={prefix} />
          {parts.map((part) => (
            <span key={part}> · {part}</span>
          ))}
        </p>
      )}
    </div>
  );
}

const control =
  "grid size-8 place-items-center border border-border bg-background text-muted-foreground outline-none transition-colors hover:border-dim hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/40";

export function ViewControls() {
  const { fitView, zoomIn, zoomOut } = useReactFlow();
  const fitOptions = useFitOptions();

  return (
    <div className="pointer-events-none absolute top-4 right-4 z-10 flex gap-1.5">
      <button
        type="button"
        aria-label="Fit the map to the view"
        onClick={() => fitView({ ...fitOptions, duration: 200 })}
        className={`pointer-events-auto ${control}`}
      >
        <ArrowsOutIcon aria-hidden className="size-4" />
      </button>
      <button
        type="button"
        aria-label="Zoom in"
        onClick={() => zoomIn({ duration: 150 })}
        className={`pointer-events-auto ${control}`}
      >
        <MagnifyingGlassPlusIcon aria-hidden className="size-4" />
      </button>
      <button
        type="button"
        aria-label="Zoom out"
        onClick={() => zoomOut({ duration: 150 })}
        className={`pointer-events-auto ${control}`}
      >
        <MagnifyingGlassMinusIcon aria-hidden className="size-4" />
      </button>
    </div>
  );
}

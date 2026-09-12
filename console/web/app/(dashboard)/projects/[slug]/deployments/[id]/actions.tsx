"use client";

import type { DeploymentKind, DeploymentOutcome } from "@console/db/schema";
import {
  ArrowCounterClockwiseIcon,
  ArrowSquareOutIcon,
  ArrowsClockwiseIcon,
} from "@phosphor-icons/react";
import { Button } from "@/components/ui/button";
import {
  Popover,
  PopoverContent,
  PopoverDescription,
  PopoverHeader,
  PopoverTitle,
  PopoverTrigger,
} from "@/components/ui/popover";
import { promotes } from "@/lib/runs";
import { CommandPane } from "../../../../command-pane";

function Deferred({
  label,
  Icon,
  command,
  why,
}: {
  label: string;
  Icon: typeof ArrowsClockwiseIcon;
  command: string;
  why: string;
}) {
  return (
    <Popover>
      <PopoverTrigger render={<Button variant="outline" />}>
        <Icon data-icon="inline-start" />
        {label}
      </PopoverTrigger>
      <PopoverContent className="w-96 max-w-[calc(100vw-2rem)] gap-3 p-4">
        <PopoverHeader>
          <PopoverTitle>Runs from your terminal</PopoverTitle>
          <PopoverDescription>{why}</PopoverDescription>
        </PopoverHeader>
        <CommandPane command={command} />
        <p className="text-xs text-muted-foreground">
          A connector in your account will let the console do this. Planned.
        </p>
      </PopoverContent>
    </Popover>
  );
}

export function RunActions({
  url,
  kind,
  outcome,
  promotionId,
  environmentClass,
  environmentIdentity,
  active,
}: {
  url: string | null;
  kind: DeploymentKind;
  outcome: DeploymentOutcome;
  promotionId: string | null;
  environmentClass: "production" | "preview";
  environmentIdentity: string;
  active: boolean;
}) {
  if (!promotes(kind)) {
    return null;
  }
  const preview = environmentClass === "preview";
  const rollback = !preview && outcome === "succeeded" && promotionId && !active;
  return (
    <div className="flex flex-wrap items-center gap-2">
      <Button
        variant="outline"
        disabled={!url}
        title={url ? undefined : "This run reported no url"}
        nativeButton={!url}
        render={
          // biome-ignore lint/a11y/useAnchorContent: Base UI renders the button children inside the anchor
          url ? <a href={url} target="_blank" rel="noreferrer" /> : undefined
        }
      >
        <ArrowSquareOutIcon data-icon="inline-start" />
        Visit
      </Button>
      {rollback && (
        <Deferred
          label="Roll back to this"
          Icon={ArrowCounterClockwiseIcon}
          command={`ocel rollback ${promotionId}`}
          why="Points production at this promotion again, without a rebuild. The rollback reports here as its own run."
        />
      )}
      <Deferred
        label="Redeploy"
        Icon={ArrowsClockwiseIcon}
        command={preview ? `ocel preview up ${environmentIdentity}`.trim() : "ocel deploy"}
        why="Builds and ships the current tree from your machine into your own cloud."
      />
    </div>
  );
}

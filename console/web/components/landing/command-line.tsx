"use client";

import { CheckIcon, CopyIcon } from "@phosphor-icons/react";
import { useState } from "react";

import { cn } from "@/lib/utils";

export function CommandLine({
  command,
  className,
}: {
  command: string;
  className?: string;
}) {
  const [copied, setCopied] = useState(false);

  function copy() {
    navigator.clipboard.writeText(command).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  }

  return (
    <button
      type="button"
      onClick={copy}
      aria-label={copied ? "Copied" : `Copy "${command}"`}
      className={cn(
        "group inline-flex items-center gap-2.5 bg-chip px-3 py-1.5 font-mono text-sm text-foreground",
        className,
      )}
    >
      <span>{command}</span>
      {copied ? (
        <CheckIcon weight="bold" className="size-3.5 text-chart-2" />
      ) : (
        <CopyIcon
          weight="bold"
          className="size-3.5 text-dim transition-colors group-hover:text-foreground"
        />
      )}
    </button>
  );
}

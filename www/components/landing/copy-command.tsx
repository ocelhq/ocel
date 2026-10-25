"use client";

import { useState } from "react";

export function CopyCommand({ command }: { command: string }) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(command);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1600);
    } catch {}
  }

  return (
    <button
      type="button"
      onClick={copy}
      className="group flex w-full min-w-0 items-start justify-between gap-3 border-[1.5px] border-foreground bg-background px-3 py-2.5 text-left font-mono text-[13px] text-foreground transition-colors hover:bg-muted"
    >
      <span className="min-w-0 break-words">
        <span className="text-muted-foreground select-none">$ </span>
        {command}
      </span>
      <span className="shrink-0 text-[11px] tracking-[0.14em] text-muted-foreground uppercase group-hover:text-foreground">
        {copied ? "Copied" : "Copy"}
      </span>
    </button>
  );
}

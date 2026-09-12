"use client";

import { CheckIcon, CopyIcon } from "@phosphor-icons/react";
import { useState } from "react";

export function CommandPane({ command }: { command: string }) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(command);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  }

  return (
    <div className="w-full max-w-md border border-terminal-rule bg-terminal font-mono text-[13px]">
      <div className="flex h-8 items-center gap-1.5 border-b border-terminal-rule px-3">
        <span aria-hidden className="size-2.5 rounded-full bg-[#ff5f57]" />
        <span aria-hidden className="size-2.5 rounded-full bg-[#febc2e]" />
        <span aria-hidden className="size-2.5 rounded-full bg-[#28c840]" />
      </div>
      <div className="flex items-center justify-between gap-3 py-2 pr-2 pl-4">
        <code className="font-mono">
          <span className="text-terminal-foreground select-none">$ </span>
          <span className="text-terminal-ink">{command}</span>
        </code>
        <button
          type="button"
          onClick={copy}
          aria-label={copied ? "Copied" : `Copy ${command}`}
          className="grid size-8 place-items-center text-terminal-foreground transition-colors outline-none hover:text-terminal-ink focus-visible:ring-2 focus-visible:ring-ring/40"
        >
          {copied ? <CheckIcon className="size-4 text-go" /> : <CopyIcon className="size-4" />}
        </button>
      </div>
    </div>
  );
}

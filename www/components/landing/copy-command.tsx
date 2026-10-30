"use client";

import { Check, Copy } from "@phosphor-icons/react";
import { useState } from "react";
import { capture } from "@/lib/analytics";

export function CopyCommand({ command, method }: { command: string; method: string }) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(command);
      capture("cli_install_command_copied", { command, method });
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1600);
    } catch {}
  }

  return (
    <button
      type="button"
      onClick={copy}
      aria-label={copied ? "Copied" : `Copy ${command}`}
      className="group flex w-full min-w-0 items-center justify-between gap-3 border-[1.5px] border-rule bg-background px-3 py-2.5 text-left font-mono text-[13px] text-foreground transition-colors hover:bg-muted"
    >
      <span className="min-w-0 break-words">
        <span className="text-muted-foreground select-none">$ </span>
        {command}
      </span>
      {copied ? (
        <Check aria-hidden size={16} className="shrink-0 text-go" />
      ) : (
        <Copy
          aria-hidden
          size={16}
          className="shrink-0 text-muted-foreground group-hover:text-foreground"
        />
      )}
    </button>
  );
}

"use client";

import { type ReactNode, useEffect, useRef, useState } from "react";
import { useTarget } from "@/components/landing/target";
import type { Line, TargetId, Tone } from "@/components/landing/transcripts";
import { cn } from "@/lib/utils";

const tone: Record<Tone, string> = {
  prompt: "text-[#7d8590] select-none",
  cmd: "text-terminal-ink",
  muted: "text-terminal-foreground",
  plain: "text-[#d4d6d9]",
  bold: "text-terminal-ink font-medium",
  ok: "text-[#3ecf7a]",
  warn: "text-[oklch(0.8_0.132_76.5)]",
  url: "text-terminal-ink underline decoration-[oklch(0.587_0.225_270.16)] decoration-[1.5px] underline-offset-4",
};

function Rendered({ lines, shown }: { lines: readonly Line[]; shown: number }) {
  const done = shown >= lines.length;
  return (
    <pre className="m-0 font-mono text-[12.5px] leading-[1.85] whitespace-pre-wrap break-words sm:text-[13px]">
      {lines.map((line, i) => (
        <span key={i} className={cn("block min-h-[1.85em]", i >= shown && "invisible")}>
          {line.map(([t, text], j) => (
            <span key={j} className={tone[t]}>
              {text}
            </span>
          ))}
        </span>
      ))}
      {done ? (
        <span aria-hidden className="block">
          <span className="text-[#7d8590]">$ </span>
          <span className="animate-blink inline-block h-[1.05em] w-[0.6em] translate-y-[0.18em] bg-electric" />
        </span>
      ) : null}
    </pre>
  );
}

export function Session({
  lines,
  byTarget,
  className,
}: {
  lines?: readonly Line[];
  byTarget?: Record<TargetId, readonly Line[]>;
  className?: string;
}) {
  const { target, run } = useTarget();
  const shown = byTarget ? byTarget[target] : (lines ?? []);
  const [count, setCount] = useState(shown.length);
  const [seen, setSeen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const io = new IntersectionObserver(
      ([entry]) => {
        if (entry?.isIntersecting) {
          setSeen(true);
          io.disconnect();
        }
      },
      { threshold: 0.35 },
    );
    io.observe(el);
    return () => io.disconnect();
  }, []);

  // biome-ignore lint/correctness/useExhaustiveDependencies: replays on a new run, not on every render
  useEffect(() => {
    if (!seen || window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      setCount(shown.length);
      return;
    }
    setCount(0);
    let i = 0;
    const tick = () => {
      i += 1;
      setCount(i);
      if (i < shown.length)
        timer = window.setTimeout(tick, shown[i]?.[0]?.[0] === "prompt" ? 260 : 55);
    };
    let timer = window.setTimeout(tick, 120);
    return () => window.clearTimeout(timer);
  }, [seen, run, target]);

  return (
    <div ref={ref} className={className} aria-live="polite">
      <Rendered lines={shown} shown={count} />
    </div>
  );
}

export function Screen({
  title,
  bar,
  children,
  className,
}: {
  title: ReactNode;
  bar?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <figure
      className={cn(
        "m-0 min-w-0 border-[1.5px] border-foreground bg-terminal shadow-float",
        className,
      )}
    >
      <div className="flex min-h-11 items-center gap-1.5 border-b border-terminal-rule px-3.5 py-2">
        <span className="size-2.5 shrink-0 rounded-full bg-[#ff5f57]" />
        <span className="size-2.5 shrink-0 rounded-full bg-[#febc2e]" />
        <span className="size-2.5 shrink-0 rounded-full bg-[#28c840]" />
        <figcaption className="ml-2 truncate font-mono text-[11px] text-terminal-foreground">
          {title}
        </figcaption>
        {bar}
      </div>
      {children}
    </figure>
  );
}

"use client";

import { createContext, type ReactNode, useContext, useEffect, useState } from "react";
import { type TargetId, targets } from "@/components/landing/transcripts";

const key = "ocel.target";

type State = { target: TargetId; setTarget: (target: TargetId) => void; run: number };

const TargetContext = createContext<State>({ target: "cloud", setTarget: () => {}, run: 0 });

export function TargetProvider({ children }: { children: ReactNode }) {
  const [target, set] = useState<TargetId>("cloud");
  const [run, setRun] = useState(0);

  useEffect(() => {
    try {
      const stored = localStorage.getItem(key);
      if (targets.some((t) => t.id === stored)) set(stored as TargetId);
    } catch {}
  }, []);

  function setTarget(next: TargetId) {
    set(next);
    setRun((n) => n + 1);
    try {
      localStorage.setItem(key, next);
    } catch {}
  }

  return (
    <TargetContext.Provider value={{ target, setTarget, run }}>{children}</TargetContext.Provider>
  );
}

export function useTarget() {
  return useContext(TargetContext);
}

export function TargetSelect({ id }: { id: string }) {
  const { target, setTarget } = useTarget();
  return (
    <label className="relative ml-auto flex items-center gap-2 font-mono text-[11px] text-terminal-foreground">
      <span className="hidden tracking-[0.14em] uppercase sm:inline">Target</span>
      <select
        id={id}
        value={target}
        onChange={(e) => setTarget(e.target.value as TargetId)}
        className="cursor-pointer appearance-none border border-electric bg-terminal py-1 pr-7 pl-2.5 font-mono text-[12px] text-terminal-ink outline-none focus-visible:ring-2 focus-visible:ring-electric/60"
      >
        {targets.map((t) => (
          <option key={t.id} value={t.id}>
            {t.status ? `${t.name} · ${t.status}` : t.name}
          </option>
        ))}
      </select>
      <svg
        aria-hidden
        viewBox="0 0 10 6"
        className="pointer-events-none absolute right-2.5 bottom-[11px] w-2 text-terminal-ink"
      >
        <path d="M1 1l4 4 4-4" fill="none" stroke="currentColor" strokeWidth="1.5" />
      </svg>
    </label>
  );
}

"use client";

import { type FormEvent, useId, useState } from "react";
import { cn } from "@/lib/utils";

const endpoint = process.env.NEXT_PUBLIC_EARLY_ACCESS_URL ?? "https://ocelhq.com/api/early-access";

type State =
  | { kind: "idle" }
  | { kind: "sending" }
  | { kind: "done"; email: string }
  | { kind: "error"; message: string };

export function EarlyAccess({
  source,
  inputId,
  className,
}: {
  source: string;
  inputId?: string;
  className?: string;
}) {
  const generated = useId();
  const id = inputId ?? generated;
  const [state, setState] = useState<State>({ kind: "idle" });

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const email = String(form.get("email") ?? "").trim();
    setState({ kind: "sending" });
    try {
      const res = await fetch(endpoint, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ email, source, company: form.get("company") ?? "" }),
      });
      const body = (await res.json().catch(() => ({}))) as { error?: string };
      if (!res.ok) {
        setState({ kind: "error", message: body.error ?? "That didn't go through. Try again." });
        return;
      }
      setState({ kind: "done", email });
    } catch {
      setState({
        kind: "error",
        message: "Couldn't reach ocelhq.com. Check your connection and try again.",
      });
    }
  }

  if (state.kind === "done") {
    return (
      <p className={cn("m-0 text-[15px] leading-relaxed text-foreground", className)} role="status">
        <svg
          aria-hidden
          viewBox="0 0 12 10"
          className="mr-1.5 inline-block w-3 -translate-y-px text-go"
        >
          <path d="M1 5.5 4.2 8.5 11 1.5" fill="none" stroke="currentColor" strokeWidth="1.8" />
        </svg>
        You're on the list. The invite goes to{" "}
        <span className="font-mono text-[13px]">{state.email}</span>.
      </p>
    );
  }

  return (
    <form onSubmit={submit} className={cn("flex flex-col gap-2", className)} noValidate={false}>
      <label htmlFor={id} className="sr-only">
        Email address
      </label>
      <div className="flex min-w-0 border-[1.5px] border-rule focus-within:ring-2 focus-within:ring-electric/40">
        <input
          id={id}
          name="email"
          type="email"
          required
          autoComplete="email"
          placeholder="you@example.com"
          className="min-w-0 flex-1 bg-background px-3 py-2.5 font-mono text-[13px] text-foreground outline-none placeholder:text-muted-foreground"
        />
        <button
          type="submit"
          disabled={state.kind === "sending"}
          className="shrink-0 bg-foreground px-4 text-[14px] font-semibold text-background transition-opacity hover:opacity-85 disabled:opacity-60"
        >
          {state.kind === "sending" ? "Sending…" : "Request access"}
        </button>
      </div>
      <input
        type="text"
        name="company"
        tabIndex={-1}
        autoComplete="off"
        aria-hidden="true"
        className="absolute -left-[9999px] h-px w-px opacity-0"
      />
      {state.kind === "error" ? (
        <p className="m-0 text-[13px] text-destructive" role="alert">
          {state.message}
        </p>
      ) : null}
    </form>
  );
}

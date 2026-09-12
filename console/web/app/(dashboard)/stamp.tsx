"use client";

import { useEffect, useState } from "react";
import { absoluteTime, relativeTime } from "@/lib/relative-time";

export function useRelative(at: string, now: string) {
  const [text, setText] = useState(() => relativeTime(at, now));

  useEffect(() => {
    const tick = () => setText(relativeTime(at, Date.now()));
    tick();
    const timer = setInterval(tick, 30_000);
    return () => clearInterval(timer);
  }, [at]);

  return text;
}

export function Stamp({ at, now, prefix }: { at: string; now: string; prefix?: string }) {
  const relative = useRelative(at, now);
  return (
    <>
      {prefix && `${prefix} `}
      <time dateTime={at} title={absoluteTime(at)}>
        {relative}
      </time>
    </>
  );
}

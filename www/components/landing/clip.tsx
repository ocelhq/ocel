"use client";

import { useEffect, useRef, useState } from "react";
import { type ClipSpec, sources } from "@/components/landing/clips";
import { Frame } from "@/components/landing/frame";
import { cn } from "@/lib/utils";

export function Pending({ clip, className }: { clip: ClipSpec; className?: string }) {
  const dark = clip.kind === "terminal";
  return (
    <div
      role="img"
      aria-label={clip.label}
      style={{ aspectRatio: clip.ratio }}
      className={cn(
        "grid w-full place-items-center p-6 text-center",
        dark ? "bg-terminal" : "bg-muted",
        className,
      )}
    >
      <div className="grid max-w-[46ch] gap-3">
        <span
          className={cn(
            "font-mono text-[11px] tracking-[0.14em] uppercase",
            dark ? "text-[#7d8590]" : "text-muted-foreground",
          )}
        >
          To be recorded
        </span>
        <p
          className={cn(
            "m-0 font-mono text-[12.5px] leading-[1.7]",
            dark ? "text-terminal-foreground" : "text-muted-foreground",
          )}
        >
          {clip.shot}
        </p>
      </div>
    </div>
  );
}

function Toggle({
  playing,
  onToggle,
  dark,
}: {
  playing: boolean;
  onToggle: () => void;
  dark: boolean;
}) {
  return (
    <button
      type="button"
      onClick={onToggle}
      className={cn(
        "text-[12px] transition-colors",
        dark
          ? "text-terminal-foreground hover:text-terminal-ink"
          : "text-muted-foreground hover:text-foreground",
      )}
    >
      {playing ? "Pause" : "Play"}
    </button>
  );
}

// A recording that behaves like a GIF: muted, looping, playing only while a third of it is
// on screen. Under reduced motion it holds on its poster until the visitor presses Play.
export function Clip({
  clip,
  className,
  flat,
}: {
  clip: ClipSpec;
  className?: string;
  flat?: boolean;
}) {
  const ref = useRef<HTMLVideoElement>(null);
  const [playing, setPlaying] = useState(false);
  const [held, setHeld] = useState(false);
  const dark = clip.kind === "terminal";

  useEffect(() => {
    const video = ref.current;
    if (!video) return;
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      setHeld(true);
      return;
    }
    const io = new IntersectionObserver(
      ([entry]) => {
        if (entry?.isIntersecting) video.play().catch(() => {});
        else video.pause();
      },
      { threshold: 0.33 },
    );
    io.observe(video);
    return () => io.disconnect();
  }, []);

  function toggle() {
    const video = ref.current;
    if (!video) return;
    if (video.paused) {
      setHeld(false);
      video.play().catch(() => {});
    } else {
      setHeld(true);
      video.pause();
    }
  }

  const src = sources(clip);
  const body = !clip.ready ? (
    <Pending clip={clip} />
  ) : clip.still ? (
    <>
      {/* biome-ignore lint/performance/noImgElement: stills come from the clips bucket, sized by their frame */}
      <img
        src={src.poster}
        alt={clip.label}
        loading="lazy"
        decoding="async"
        style={{ aspectRatio: clip.ratio }}
        className={cn("block w-full object-cover object-top", clip.dark && "dark:hidden")}
      />
      {clip.dark ? (
        // biome-ignore lint/performance/noImgElement: stills come from the clips bucket, sized by their frame
        <img
          src={src.posterDark}
          alt={clip.label}
          loading="lazy"
          decoding="async"
          style={{ aspectRatio: clip.ratio }}
          className="hidden w-full object-cover object-top dark:block"
        />
      ) : null}
    </>
  ) : (
    <video
      ref={ref}
      muted
      loop
      playsInline
      preload="none"
      poster={src.poster}
      aria-label={clip.label}
      onPlay={() => setPlaying(true)}
      onPause={() => setPlaying(false)}
      style={{ aspectRatio: clip.ratio }}
      className="block w-full object-cover"
    >
      <source src={src.webm} type="video/webm" />
      <source src={src.mp4} type="video/mp4" />
    </video>
  );

  return (
    <Frame
      kind={clip.kind}
      title={clip.title}
      flat={flat}
      className={className}
      aside={
        clip.ready && !clip.still ? (
          <Toggle playing={playing && !held} onToggle={toggle} dark={dark} />
        ) : null
      }
    >
      {body}
    </Frame>
  );
}

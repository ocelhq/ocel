type WordmarkProps = {
  markSize?: number;
  textSize?: number;
};

export function Wordmark({ markSize = 20, textSize = 17 }: WordmarkProps) {
  const hole = markSize * 0.225;
  return (
    <span className="flex items-center gap-[9px]">
      <span
        aria-hidden
        className="inline-block bg-primary"
        style={{
          width: markSize,
          height: markSize,
          borderRadius: "50%",
          clipPath: "polygon(0 0,68% 0,50% 50%,100% 38%,100% 100%,0 100%)",
          WebkitMaskImage: `radial-gradient(circle, transparent ${hole}px, #000 ${hole + 0.5}px)`,
          maskImage: `radial-gradient(circle, transparent ${hole}px, #000 ${hole + 0.5}px)`,
        }}
      />
      <span
        className="font-display text-foreground"
        style={{ fontSize: textSize, letterSpacing: "-0.04em" }}
      >
        ocel
      </span>
    </span>
  );
}

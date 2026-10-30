export type Tone = "prompt" | "cmd" | "muted" | "ok" | "url";
export type Line = [Tone, string][];

const toneClass: Record<Tone, string> = {
  prompt: "text-[#7d8590] select-none",
  cmd: "text-terminal-ink",
  muted: "text-terminal-foreground",
  ok: "text-[#3ecf7a]",
  url: "text-terminal-ink underline decoration-[oklch(0.587_0.225_270.16)] decoration-[1.5px] underline-offset-4",
};

// Terminal output as real text, one span per tone.
export function Transcript({ lines }: { lines: Line[] }) {
  return (
    <pre className="m-0 overflow-x-auto px-4 py-4 font-mono text-[12.5px] leading-[1.85] sm:px-5 sm:text-[13px]">
      {lines.map((line, i) => (
        <span key={i} className="block min-h-[1.85em]">
          {line.map(([tone, text], j) => (
            <span key={j} className={toneClass[tone]}>
              {text}
            </span>
          ))}
        </span>
      ))}
    </pre>
  );
}

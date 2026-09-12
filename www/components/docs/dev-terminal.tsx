export function DevTerminal() {
  return (
    <div className="not-prose my-6 max-w-xl border border-[#2a2d31] bg-[#16181a] font-mono text-[0.84375rem] leading-[1.9] text-[#9aa0a8]">
      <div className="flex items-center gap-2 border-b border-[#2a2d31] px-4 py-2.5">
        <span className="size-2.5 bg-[#ff5f57]" />
        <span className="size-2.5 bg-[#febc2e]" />
        <span className="size-2.5 bg-[#28c840]" />
      </div>
      <pre className="overflow-x-auto px-4 py-3">
        <span className="text-[#7d8590]">$</span>{" "}
        <span className="text-[#f2f1ec]">ocel dev -- next dev</span>
        {"\n\n"}
        <span className="text-[#3ecf7a]">✓</span> Resolved 1 resource:{" "}
        <span className="text-[#f2f1ec]">postgres("main")</span>
        {"\n"}
        <span className="text-[#3ecf7a]">✓</span> Connected, ready in{" "}
        <span className="text-[#f2f1ec]">0.4s</span>
        {"\n\n"}
        {"  "}
        <span className="text-[#f2f1ec]">▲ Next.js 16.0.0</span>
        {"\n"}
        {"  "}- Local: <span className="text-[#f2f1ec]">http://localhost:3000</span>
      </pre>
    </div>
  );
}

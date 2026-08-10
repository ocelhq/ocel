const items = [
  "ZERO CONFIG",
  "NO MARKUP",
  "NO LOCK-IN",
  "REAL INFRA IN DEV",
  "YOUR ACCOUNT, YOUR NAME",
];

function Group() {
  return (
    <span className="text-[13px] font-semibold tracking-[0.08em] text-background">
      {items.map((item) => (
        <span key={item}>
          {item}
          &nbsp;&nbsp;<span className="text-primary">✕</span>&nbsp;&nbsp;
        </span>
      ))}
    </span>
  );
}

export function Marquee() {
  return (
    <div className="overflow-hidden whitespace-nowrap border-t-[1.5px] border-foreground bg-foreground py-[11px]">
      <div className="flex w-max animate-marquee">
        {["a", "b", "c", "d", "e", "f"].map((id) => (
          <Group key={id} />
        ))}
      </div>
    </div>
  );
}

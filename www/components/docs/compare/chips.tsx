import Link from "next/link";

const CHIPS = [
  { slug: "vercel", name: "Vercel" },
  { slug: "railway", name: "Railway" },
  { slug: "fly", name: "Fly.io" },
  { slug: "terraform", name: "Terraform" },
  { slug: "pulumi", name: "Pulumi" },
  { slug: "sst", name: "SST" },
  { slug: "coolify", name: "Coolify" },
  { slug: "kamal", name: "Kamal" },
];

const CHIP =
  "border border-(--hairline) bg-(--paper) px-2 py-1.5 font-mono text-[0.6875rem] font-medium uppercase leading-none tracking-[0.14em] text-(--steel) no-underline hover:border-(--steel)";

export function CompareChips() {
  return (
    <div className="not-prose my-6 flex flex-wrap items-center gap-2 text-(--body)">
      <span className="me-1 text-base leading-none">How is this different from</span>
      {CHIPS.map((chip) => (
        <Link key={chip.slug} href={`/docs/compare?vs=${chip.slug}`} className={CHIP}>
          {chip.name}
        </Link>
      ))}
      <Link href="/docs/compare" className={CHIP}>
        More →
      </Link>
    </div>
  );
}

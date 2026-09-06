import Link from "next/link";
import { OverviewDiagram } from "@/components/overview-diagram";

const inline = "border-b-[1.5px] border-(--electric) text-(--ink) no-underline";

export function Hero() {
  return (
    <section className="not-prose flex flex-col gap-8 border-b border-(--hairline) pt-4 pb-12 lg:flex-row lg:items-center lg:justify-between lg:gap-12">
      <div className="max-w-2xl ">
        <h1 className="text-balance font-display text-[2.5rem] font-semibold leading-[1.1] tracking-[-0.035em] text-(--ink)">
          Ocel Documentation
        </h1>
        <p className="mt-5 max-w-[34ch] text-lg leading-[1.55] text-(--body)">
          Ocel deploys{" "}
          <Link href="/docs/frameworks/nextjs" className={inline}>
            apps
          </Link>{" "}
          to your own{" "}
          <Link href="/docs/providers" className={inline}>
            cloud
          </Link>{" "}
          or servers with one command.
        </p>
        <Link
          href="/docs/quick-start"
          className="mt-8 inline-flex items-center border border-(--ink) bg-(--ink) px-4 py-2.5 text-[0.9375rem] font-medium leading-none text-(--paper) no-underline"
        >
          Deploy now
        </Link>
      </div>
      <OverviewDiagram className="order-first w-full max-w-[24rem] shrink-0 self-start lg:order-none lg:self-auto" />
    </section>
  );
}

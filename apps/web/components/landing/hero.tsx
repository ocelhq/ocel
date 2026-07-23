"use client";

import { useState } from "react";
import { Cursor, Terminal } from "@/components/landing/terminal";
import { GithubLogo } from "@/components/landing/tool-logos";
import { Button } from "@/components/ui/button";

export function Hero() {
  const [step, setStep] = useState(0);

  function start() {
    if (step) return;
    setStep(1);
    setTimeout(() => setStep(2), 700);
    setTimeout(() => setStep(3), 1600);
  }

  return (
    <section
      className="relative overflow-hidden"
      style={{
        backgroundImage:
          "linear-gradient(var(--grid) 1px,transparent 1px),linear-gradient(90deg,var(--grid) 1px,transparent 1px)",
        backgroundSize: "48px 48px",
      }}
    >
      <div
        className="absolute inset-0"
        style={{
          background:
            "radial-gradient(ellipse 75% 95% at 62% 45%,transparent 25%,var(--background) 80%)",
        }}
      />
      <span className="absolute left-[41%] top-[30px] font-mono text-[15px] text-primary">
        +
      </span>
      <span className="absolute right-16 top-9 font-mono text-[15px] text-faint">
        +
      </span>
      <span className="absolute bottom-10 left-9 font-mono text-[15px] text-faint">
        +
      </span>
      <span className="absolute bottom-[52px] right-[36%] font-mono text-[15px] text-primary">
        +
      </span>
      <span className="absolute right-[-34px] top-1/2 origin-center rotate-90 font-mono text-[10.5px] tracking-[0.14em] text-dim">
        FIG. 01 — YOUR ACCOUNT
      </span>

      <div className="relative mx-auto grid max-w-[1180px] grid-cols-1 gap-11 px-10 pb-[58px] pt-16 md:grid-cols-[1fr_1.06fr]">
        <div>
          <div className="mb-[18px] font-mono text-xs tracking-[0.08em] text-primary">
            $ npm i -g ocel
          </div>
          <h1 className="text-[54px] font-semibold leading-[1.04] tracking-[-0.03em] text-foreground text-pretty">
            Vercel DX.
            <br />
            <span className="text-primary">Your cloud.</span>
          </h1>
          <p className="mt-5 max-w-[44ch] text-[16.5px] leading-[1.62] text-muted-foreground">
            The deploy experience you love, running in the account you already
            pay for. Zero-config deploys, real dev infra, and an SDK that turns{" "}
            <span className="bg-chip px-[5px] py-px font-mono text-[14.5px]">
              postgres("main")
            </span>{" "}
            into a database.
          </p>
          <div className="mt-7 flex items-center gap-3">
            <Button className="h-auto rounded-none px-6 py-[13px] text-[15px] font-semibold normal-case tracking-normal">
              Start deploying
            </Button>
            <Button
              variant="outline"
              className="h-auto rounded-none border-[1.5px] border-foreground bg-background px-[18px] py-[11px] font-mono text-sm font-normal normal-case tracking-normal text-foreground"
            >
              ocel init ↵
            </Button>
          </div>
        </div>

        <Terminal
          title="acme — ocel deploy"
          onClick={start}
          className="self-start"
          bodyClassName="min-h-[196px] px-5.5 py-[18px] text-[13.5px] leading-[1.9] text-foreground"
        >
          <div>
            <span className="text-dim">$</span> ocel deploy
          </div>
          <div>
            <span className="text-chart-2">✓</span> Build{" "}
            <span className="text-dim">·············</span> 12s
          </div>
          <div>
            <span className="text-chart-2">✓</span> Provision{" "}
            <span className="text-dim">·········</span> 8s{" "}
            <span className="text-dim">postgres · bucket · queue</span>
          </div>
          <div>
            <span className="text-chart-2">✓</span> Release{" "}
            <span className="text-dim">···········</span> 3s
          </div>
          <div>
            <span className="text-primary">→</span> https://api.acme.dev{" "}
            <span className="text-dim">deployed to</span> aws:acme-prod
          </div>
          {step === 0 && (
            <div className="mt-2.5">
              <GithubLogo className="mr-1.5 inline-block h-[15px] w-[15px] -translate-y-px align-middle text-foreground" />
              get started — sign in with Github?{" "}
              <span className="text-dim">[Y/n]</span>
              <Cursor /> <span className="text-faint">← click to approve</span>
            </div>
          )}
          {step >= 1 && (
            <div className="mt-2.5">
              <GithubLogo className="mr-1.5 inline-block h-[15px] w-[15px] -translate-y-px align-middle text-foreground" />
              get started — sign in with Github?{" "}
              <span className="text-dim">[Y/n]</span> y
            </div>
          )}
          {step >= 2 && (
            <div>
              <span className="text-primary">→</span> opening
              github.com/login/oauth …
            </div>
          )}
          {step >= 3 && (
            <>
              <div>
                <span className="text-chart-2">✓</span> authenticated as{" "}
                <span className="bg-chip px-1">@you</span>{" "}
                <span className="text-dim">
                  —{" "}
                  <span className="text-primary">
                    heading to your console →
                  </span>
                </span>
              </div>
              <div>
                <span className="text-dim">$</span>
                <Cursor />
              </div>
            </>
          )}
        </Terminal>
      </div>
    </section>
  );
}

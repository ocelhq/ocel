"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import { Wordmark } from "@/components/landing/wordmark";
import { Button } from "@/components/ui/button";

const navLinks = [
  { label: "Docs", href: "#" },
  { label: "CLI", href: "#" },
  { label: "SDK", href: "#" },
  { label: "Blog", href: "#" },
];

export function SiteHeader() {
  const [dark, setDark] = useState(false);

  useEffect(() => {
    setDark(document.documentElement.classList.contains("dark"));
  }, []);

  function toggle() {
    const next = !dark;
    setDark(next);
    document.documentElement.classList.toggle("dark", next);
  }

  return (
    <header className="sticky top-0 z-50 border-b-[1.5px] border-foreground bg-background/85 backdrop-blur-md">
      <div className="mx-auto flex max-w-[1180px] items-center gap-[26px] px-10 py-4">
        <Wordmark />
        {navLinks.map((link) => (
          <Link
            key={link.label}
            href={link.href}
            className="text-[13px] font-medium text-muted-foreground"
          >
            {link.label}
          </Link>
        ))}
        <span className="ml-auto font-mono text-xs tracking-[0.08em] text-dim">
          ALPHA
        </span>
        <button
          type="button"
          onClick={toggle}
          className="border border-border px-2.5 py-[5px] font-mono text-[11px] text-dim"
        >
          ◐ {dark ? "light" : "dark"}
        </button>
        <Link href="#" className="text-[13px] font-medium text-foreground">
          Sign in
        </Link>
        <Button
          render={<Link href="#" />}
          className="h-auto rounded-none bg-foreground px-4 py-2 text-[13px] font-semibold tracking-normal text-background normal-case hover:bg-foreground/85"
        >
          Get started
        </Button>
      </div>
    </header>
  );
}

import { DocsLayout } from "fumadocs-ui/layouts/docs";
import { RootProvider } from "fumadocs-ui/provider/next";
import type { Metadata } from "next";
import { Archivo, IBM_Plex_Mono, IBM_Plex_Sans, Space_Grotesk } from "next/font/google";
import type { CSSProperties, ReactNode } from "react";
import { baseOptions } from "@/lib/layout.shared";
import { layoutTree, tabColors } from "@/lib/source";
import "./globals.css";

const grotesk = Space_Grotesk({
  subsets: ["latin"],
  weight: ["400", "500", "600"],
  variable: "--font-grotesk",
});
const plexSans = IBM_Plex_Sans({
  subsets: ["latin"],
  weight: ["400", "500", "600"],
  variable: "--font-plex-sans",
});
const plexMono = IBM_Plex_Mono({
  subsets: ["latin"],
  weight: ["400", "500"],
  variable: "--font-plex-mono",
});
const archivo = Archivo({ subsets: ["latin"], weight: ["800"], variable: "--font-archivo" });

export const metadata: Metadata = {
  title: { default: "Ocel Docs", template: "%s - Ocel Docs" },
};

export default function Layout({ children }: { children: ReactNode }) {
  return (
    <html
      lang="en"
      suppressHydrationWarning
      className={`${grotesk.variable} ${plexSans.variable} ${plexMono.variable} ${archivo.variable}`}
    >
      <body className="flex min-h-screen flex-col bg-(--paper) font-sans text-(--ink) antialiased">
        <RootProvider theme={{ attribute: ["class", "data-theme"] }}>
          <DocsLayout
            tree={layoutTree()}
            {...baseOptions()}
            tabs={{
              transform(option, node) {
                if (!node.icon || !node.$id) return option;
                return {
                  ...option,
                  icon: (
                    <div
                      className="size-full text-(--tab-color) [&_svg]:size-full"
                      style={{ "--tab-color": tabColors[node.$id] } as CSSProperties}
                    >
                      {node.icon}
                    </div>
                  ),
                };
              },
            }}
          >
            {children}
          </DocsLayout>
        </RootProvider>
        {/* impeccable-live-start */}
        <script src="http://localhost:8400/live.js?token=5f407778-aece-475d-9642-2860a1d58ca4"></script>
        {/* impeccable-live-end */}
      </body>
    </html>
  );
}

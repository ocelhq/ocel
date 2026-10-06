import { DocsLayout } from "fumadocs-ui/layouts/docs";
import { RootProvider } from "fumadocs-ui/provider/next";
import type { Metadata } from "next";
import localFont from "next/font/local";
import type { CSSProperties, ReactNode } from "react";
import { baseOptions } from "@/lib/layout.shared";
import { layoutTree, tabColors } from "@/lib/source";
import "./globals.css";

const grotesk = localFont({
  src: "../../../node_modules/@ocelhq/theme/fonts/space-grotesk/space-grotesk-latin-300-700.woff2",
  weight: "400 600",
  variable: "--font-grotesk",
});
const plexSans = localFont({
  src: "../../../node_modules/@ocelhq/theme/fonts/ibm-plex-sans/ibm-plex-sans-latin-100-700.woff2",
  weight: "400 600",
  variable: "--font-plex-sans",
});
const plexMono = localFont({
  src: [
    {
      path: "../../../node_modules/@ocelhq/theme/fonts/ibm-plex-mono/ibm-plex-mono-latin-400.woff2",
      weight: "400",
    },
    {
      path: "../../../node_modules/@ocelhq/theme/fonts/ibm-plex-mono/ibm-plex-mono-latin-500.woff2",
      weight: "500",
    },
  ],
  variable: "--font-plex-mono",
});
const archivo = localFont({
  src: "../../../node_modules/@ocelhq/theme/fonts/archivo/archivo-latin-800.woff2",
  weight: "800",
  variable: "--font-archivo",
});

export const metadata: Metadata = {
  title: { default: "Ocel Docs", template: "%s - Ocel Docs" },
};

export default function Layout({ children }: { children: ReactNode }) {
  return (
    <html
      lang="en"
      data-register="read"
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
        {process.env.NODE_ENV === "development" && (
          <script src="http://localhost:8400/live.js?token=5f407778-aece-475d-9642-2860a1d58ca4"></script>
        )}
        {/* impeccable-live-end */}
      </body>
    </html>
  );
}

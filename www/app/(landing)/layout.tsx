import type { Metadata } from "next";
import localFont from "next/font/local";
import "./globals.css";
import { cn } from "@/lib/utils";

const spaceGrotesk = localFont({
  src: "../../../ui/theme/fonts/space-grotesk/space-grotesk-latin-300-700.woff2",
  weight: "300 700",
  variable: "--font-sans",
});
const ibmPlexMono = localFont({
  src: [
    { path: "../../../ui/theme/fonts/ibm-plex-mono/ibm-plex-mono-latin-400.woff2", weight: "400" },
    { path: "../../../ui/theme/fonts/ibm-plex-mono/ibm-plex-mono-latin-500.woff2", weight: "500" },
    { path: "../../../ui/theme/fonts/ibm-plex-mono/ibm-plex-mono-latin-600.woff2", weight: "600" },
  ],
  variable: "--font-mono",
});
const archivo = localFont({
  src: "../../../ui/theme/fonts/archivo/archivo-latin-800.woff2",
  weight: "800",
  variable: "--font-display",
});

export const metadata: Metadata = {
  title: "Ocel — Deploy apps to your own cloud",
  description:
    'The deploy experience you love, running in the account you already pay for. Zero-config deploys, real dev infra, and an SDK that turns postgres("main") into a database.',
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html
      lang="en"
      data-register="landing"
      className={cn(
        "h-full",
        "antialiased",
        "font-sans",
        spaceGrotesk.variable,
        ibmPlexMono.variable,
        archivo.variable,
      )}
    >
      <body className="min-h-full flex flex-col">{children}</body>
    </html>
  );
}

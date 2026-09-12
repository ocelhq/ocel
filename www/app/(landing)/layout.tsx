import type { Metadata } from "next";
import { Archivo, IBM_Plex_Mono, Space_Grotesk } from "next/font/google";
import "./globals.css";
import { cn } from "@/lib/utils";

const spaceGrotesk = Space_Grotesk({
  subsets: ["latin"],
  variable: "--font-sans",
});
const ibmPlexMono = IBM_Plex_Mono({
  weight: ["400", "500", "600"],
  subsets: ["latin"],
  variable: "--font-mono",
});
const archivo = Archivo({
  weight: ["800"],
  subsets: ["latin"],
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

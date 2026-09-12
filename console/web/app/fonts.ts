import { Archivo, IBM_Plex_Mono, IBM_Plex_Sans, Space_Grotesk } from "next/font/google";

const body = IBM_Plex_Sans({
  weight: ["400", "500", "600"],
  subsets: ["latin"],
  variable: "--font-sans",
});

const heading = Space_Grotesk({
  weight: ["500", "600", "700"],
  subsets: ["latin"],
  variable: "--font-heading",
});

const mono = IBM_Plex_Mono({
  weight: ["400", "500", "600"],
  subsets: ["latin"],
  variable: "--font-mono",
});

const display = Archivo({
  weight: ["800"],
  subsets: ["latin"],
  variable: "--font-display",
});

export const fontVariables = [body, heading, mono, display].map((font) => font.variable).join(" ");

// Every recording on the landing page. Each one is a file stem under clipsBase:
// `<id>.mp4` (H.264) and `<id>.webm` for videos, `<id>.jpg` for posters and stills, and
// `<id>-dark.jpg` for a still taken in the dark theme too.
// A clip with `ready: false` draws its shot description in place of the video, so the
// page builds and reads before the recording exists. Flip it once the files are uploaded.

export const clipsBase = process.env.NEXT_PUBLIC_CLIPS_URL ?? "/clips";

export type ClipKind = "terminal" | "browser";

export type ClipSpec = {
  id: string;
  kind: ClipKind;
  /** The frame's title bar: a working directory for a terminal, a URL for a browser. */
  title: string;
  /** What happens in the clip, for screen readers and the reduced-motion poster. */
  label: string;
  /** What to record, shown in place of the clip until it is ready. */
  shot: string;
  ratio: "16/9" | "16/10";
  ready: boolean;
  /** A still image rather than a video. */
  still?: boolean;
  /** The still also has a dark-theme take. */
  dark?: boolean;
};

export function sources(clip: ClipSpec) {
  const stem = `${clipsBase}/${clip.id}`;
  return {
    mp4: `${stem}.mp4`,
    webm: `${stem}.webm`,
    poster: `${stem}.jpg`,
    posterDark: `${stem}-dark.jpg`,
  };
}

export const clips = {
  console: {
    id: "console",
    kind: "browser",
    title: "app.ocelhq.com",
    label:
      "The console's project list, with projects on Ocel Cloud, AWS, Google Cloud and a VPS side by side.",
    shot: "A screenshot of the console at 1440×900 (2×), saved as console.jpg and console-dark.jpg: the project list with Ocel Cloud, AWS, GCP and VPS projects.",
    ratio: "16/10",
    ready: true,
    still: true,
    dark: true,
  },
} satisfies Record<string, ClipSpec>;

const formatter = new Intl.RelativeTimeFormat("en", { numeric: "auto" });

const units = [
  ["year", 31536000],
  ["month", 2592000],
  ["week", 604800],
  ["day", 86400],
  ["hour", 3600],
  ["minute", 60],
] as const;

export function relativeTime(value: Date | string | number, now: Date | string | number): string {
  const then = new Date(value).getTime();
  const seconds = Math.round((then - new Date(now).getTime()) / 1000);
  const size = Math.abs(seconds);

  for (const [unit, span] of units) {
    if (size >= span) {
      return formatter.format(Math.round(seconds / span), unit);
    }
  }
  return formatter.format(seconds, "second");
}

const absoluteFormat = new Intl.DateTimeFormat("en", {
  dateStyle: "medium",
  timeStyle: "short",
  timeZone: "UTC",
});

export function absoluteTime(value: Date | string | number): string {
  return `${absoluteFormat.format(new Date(value))} UTC`;
}

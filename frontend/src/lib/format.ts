import type { Asset } from "#/lib/api";

export const dayFormat = new Intl.DateTimeFormat(undefined, {
  weekday: "long",
  year: "numeric",
  month: "long",
  day: "numeric",
});

/** The capture time to display: wall clock when known, else the UTC instant. */
export function captureTime(a: Asset): number | undefined {
  return a.localDateTime ?? a.dateTime;
}

export function formatDuration(ms: number | undefined): string {
  if (!ms) return "";
  const total = Math.round(ms / 1000);
  const m = Math.floor(total / 60);
  const s = total % 60;
  return `${m}:${String(s).padStart(2, "0")}`;
}

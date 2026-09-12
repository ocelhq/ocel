export const SIDEBAR_STATE_COOKIE = "sidebar_state";
export const SIDEBAR_WIDTH_COOKIE = "sidebar_width";
export const SIDEBAR_WIDTH_DEFAULT = 256;
export const SIDEBAR_WIDTH_MIN = 208;
export const SIDEBAR_WIDTH_MAX = 448;
export const SIDEBAR_COLLAPSE_AT = 144;

export function sidebarWidth(value: number): number {
  if (!Number.isFinite(value)) {
    return SIDEBAR_WIDTH_DEFAULT;
  }
  return Math.min(Math.max(Math.round(value), SIDEBAR_WIDTH_MIN), SIDEBAR_WIDTH_MAX);
}

import { ImageResponse } from "next/og";

export const ogSize = { width: 1200, height: 630 };
export const ogContentType = "image/png";

const cutRing = "M66.94 2.96 A50 50 0 1 0 98.62 38.33 L72.36 44.63 A23 23 0 1 1 57.79 28.36 Z";

export function ogImage({ title, description }: { title: string; description?: string }) {
  return new ImageResponse(
    <div
      style={{
        width: "100%",
        height: "100%",
        display: "flex",
        flexDirection: "column",
        justifyContent: "space-between",
        padding: "72px 80px",
        background: "#16181a",
        color: "#f2f1ec",
      }}
    >
      <div style={{ display: "flex", alignItems: "center", gap: 20 }}>
        <svg width="56" height="56" viewBox="0 0 100 100" aria-hidden="true">
          <path fill="#4d68ff" d={cutRing} />
        </svg>
        <div style={{ fontSize: 52, fontWeight: 800, letterSpacing: -2 }}>ocel</div>
        <div
          style={{
            marginLeft: 12,
            padding: "6px 10px",
            background: "#e0a44a",
            color: "#16181a",
            fontSize: 20,
            fontWeight: 700,
            letterSpacing: 3,
          }}
        >
          ALPHA
        </div>
      </div>
      <div style={{ display: "flex", flexDirection: "column", gap: 20 }}>
        <div style={{ fontSize: 68, fontWeight: 700, lineHeight: 1.1, letterSpacing: -2 }}>
          {title}
        </div>
        {description ? (
          <div style={{ fontSize: 32, lineHeight: 1.35, color: "#9aa0a8" }}>{description}</div>
        ) : null}
      </div>
      <div style={{ display: "flex", fontSize: 26, color: "#9aa0a8" }}>ocel.dev</div>
    </div>,
    ogSize,
  );
}

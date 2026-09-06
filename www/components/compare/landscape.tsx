"use client";

import type { ComponentType, SVGProps } from "react";
import { cutRing } from "@/components/logo";
import {
  CoolifyLogo,
  DockerLogo,
  FlyLogo,
  PulumiLogo,
  RailwayLogo,
  RenderLogo,
  SstLogo,
  TerraformLogo,
  VercelLogo,
} from "@/components/logos";
import type { Account, LandscapeData, LandscapePoint } from "./data";

const W = 480;
const H = 348;
const TOP = 26;
const BAND = 98;
const AXIS = TOP + BAND * 3;
const TILE = 34;
const LOGO = 18;
const ROWS = [8, 53];
const NAME_DROP = 9;
const FONT = 10;
const TRACK = 1.4;
const RING = 26;

const Y_AXIS = 110;
const X0 = 132;
const X1 = 452;

const BANDS: { account: Account; name: string[] }[] = [
  { account: "server", name: ["Your server"] },
  { account: "cloud", name: ["Your cloud"] },
  { account: "vendor", name: ["Managed"] },
];

const LOGOS: Record<string, ComponentType<SVGProps<SVGSVGElement>>> = {
  vercel: VercelLogo,
  railway: RailwayLogo,
  render: RenderLogo,
  fly: FlyLogo,
  terraform: TerraformLogo,
  pulumi: PulumiLogo,
  sst: SstLogo,
  coolify: CoolifyLogo,
  compose: DockerLogo,
};

function scale(dx: number) {
  return X0 + (dx / 100) * (X1 - X0);
}

function bandTop(account: Account) {
  return TOP + BANDS.findIndex((band) => band.account === account) * BAND;
}

type Placed = LandscapePoint & { x: number; y: number };

function place(points: LandscapePoint[]): Placed[] {
  return BANDS.flatMap((band) => {
    const top = bandTop(band.account);
    return points
      .filter((point) => point.account === band.account)
      .map((point) => ({ ...point, x: scale(point.dx) }))
      .sort((a, b) => a.x - b.x)
      .map((point, index) => ({ ...point, y: top + ROWS[index % 2] + TILE / 2 }));
  });
}

function connector(mark: Placed, ocelX: number) {
  const dx = ocelX - mark.x;
  const dy = TOP + BAND - mark.y;
  const length = Math.hypot(dx, dy);
  if (length === 0) return null;
  const half = TILE / 2;
  const edge = Math.min(
    dx === 0 ? Infinity : Math.abs(half / dx),
    dy === 0 ? Infinity : Math.abs(half / dy),
  );
  const gap = RING / 2 / length;
  if (edge + gap >= 1) return null;
  return {
    x1: mark.x + dx * edge,
    y1: mark.y + dy * edge,
    x2: ocelX - dx * gap,
    y2: TOP + BAND - dy * gap,
  };
}

function Label({
  x,
  y,
  anchor,
  tone,
  children,
}: {
  x: number;
  y: number;
  anchor?: "middle" | "end";
  tone: string;
  children: string;
}) {
  return (
    <text
      x={x}
      y={y}
      fill={tone}
      textAnchor={anchor}
      fontSize={FONT}
      letterSpacing={TRACK}
      className="font-mono font-medium uppercase"
    >
      {children}
    </text>
  );
}

function Mark({
  mark,
  selected,
  onSelect,
}: {
  mark: Placed;
  selected: boolean;
  onSelect: (slug: string) => void;
}) {
  const Logo = LOGOS[mark.slug];
  const half = TILE / 2;
  const tone = selected
    ? ""
    : " opacity-65 group-hover/mark:opacity-100 group-focus-visible/mark:opacity-100";
  const stroke = selected
    ? "stroke-(--ink)"
    : "stroke-(--steel) group-hover/mark:stroke-(--ink) group-focus-visible/mark:stroke-(--ink)";
  const ink = selected
    ? "text-(--ink)"
    : "text-(--steel) group-hover/mark:text-(--ink) group-focus-visible/mark:text-(--ink)";
  const motion =
    " motion-safe:transition-[opacity,stroke,color] motion-safe:duration-200 motion-safe:ease-out";

  return (
    <a
      href={`?vs=${mark.slug}`}
      aria-label={`Compare with ${mark.name}`}
      aria-current={selected ? "true" : undefined}
      className="group/mark landscape-mark outline-none"
      onClick={(event) => {
        event.preventDefault();
        onSelect(mark.slug);
      }}
    >
      <rect x={mark.x - half} y={mark.y - half} width={TILE} height={TILE} fill="var(--paper)" />
      <g className={tone + motion}>
        <rect
          x={mark.x - half}
          y={mark.y - half}
          width={TILE}
          height={TILE}
          fill="none"
          strokeWidth="1"
          className={stroke + motion}
        />
        {Logo ? (
          <Logo
            x={mark.x - LOGO / 2}
            y={mark.y - LOGO / 2}
            width={LOGO}
            height={LOGO}
            className={ink + motion}
          />
        ) : (
          <text
            x={mark.x}
            y={mark.y + 3}
            textAnchor="middle"
            fill="currentColor"
            fontSize={7}
            letterSpacing={0.5}
            className={`font-mono font-medium uppercase ${ink}${motion}`}
          >
            {mark.name}
          </text>
        )}
        <text
          x={mark.x}
          y={mark.y + half + NAME_DROP}
          textAnchor="middle"
          fill="var(--ink)"
          fontSize={FONT}
          letterSpacing={TRACK}
          className={`font-mono font-medium uppercase ${
            selected
              ? ""
              : "opacity-0 group-hover/mark:opacity-100 group-focus-visible/mark:opacity-100"
          }${motion}`}
        >
          {mark.name}
        </text>
      </g>
      <rect
        x={mark.x - half - 2}
        y={mark.y - half - 2}
        width={TILE + 4}
        height={TILE + 4}
        fill="none"
        stroke="var(--electric)"
        strokeWidth="2"
        className="landscape-ring"
      />
      <rect x={mark.x - 22} y={mark.y - 22} width={44} height={44} fill="transparent" />
    </a>
  );
}

export function Landscape({
  data,
  selected,
  onSelect,
}: {
  data: LandscapeData;
  selected: string;
  onSelect: (slug: string) => void;
}) {
  const marks = place(data.points);
  const chosen = marks.find((mark) => mark.slug === selected);
  const ocelX = scale(data.ocel.dx);
  const line = chosen ? connector(chosen, ocelX) : null;

  return (
    <figure className="m-0">
      <svg viewBox={`0 0 ${W} ${H}`} width="100%" className="block">
        <title>Where each tool sits: who holds the account, and the developer experience</title>

        <g stroke="var(--hairline)" strokeWidth="1">
          <line x1={Y_AXIS} x2={W} y1={TOP + BAND} y2={TOP + BAND} />
          <line x1={Y_AXIS} x2={W} y1={TOP + BAND * 2} y2={TOP + BAND * 2} />
        </g>

        <g fill="none" stroke="var(--steel)" strokeWidth="1">
          <line x1={Y_AXIS} x2={W - 8} y1={AXIS} y2={AXIS} />
          <path d={`M ${W - 14} ${AXIS - 5} L ${W - 8} ${AXIS} L ${W - 14} ${AXIS + 5}`} />
          <line x1={Y_AXIS} x2={Y_AXIS} y1={AXIS} y2={TOP - 4} />
          <path
            d={`M ${Y_AXIS - 5} ${TOP + 2} L ${Y_AXIS} ${TOP - 4} L ${Y_AXIS + 5} ${TOP + 2}`}
          />
        </g>

        <Label x={Y_AXIS + 10} y={14} tone="var(--steel)">
          Control & Ownership
        </Label>

        {BANDS.map((band) =>
          band.name.map((line_, index) => (
            <Label
              key={line_}
              x={Y_AXIS - 10}
              y={bandTop(band.account) + BAND / 2 - (band.name.length - 1) * 7 + index * 14}
              anchor="end"
              tone="var(--steel)"
            >
              {line_}
            </Label>
          )),
        )}

        <Label x={W - 8} y={AXIS + 18} anchor="end" tone="var(--steel)">
          Better dx
        </Label>

        {line ? (
          <line
            x1={line.x1}
            y1={line.y1}
            x2={line.x2}
            y2={line.y2}
            stroke="var(--steel)"
            strokeWidth="1.5"
            strokeDasharray="6 6"
            opacity="0.6"
          />
        ) : null}

        <g
          transform={`translate(${ocelX - RING / 2} ${TOP + BAND - RING / 2}) scale(${RING / 100})`}
        >
          <path fill="var(--electric)" d={cutRing} />
        </g>
        <Label x={ocelX + RING / 2 + 6} y={TOP + BAND + 3} tone="var(--ink)">
          Ocel
        </Label>

        {marks
          .filter((mark) => mark.slug !== selected)
          .map((mark) => (
            <Mark key={mark.slug} mark={mark} selected={false} onSelect={onSelect} />
          ))}
        {chosen ? <Mark key={chosen.slug} mark={chosen} selected onSelect={onSelect} /> : null}
      </svg>
    </figure>
  );
}

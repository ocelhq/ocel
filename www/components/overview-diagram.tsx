import type { ComponentType, SVGProps } from "react";
import {
  AwsLogo,
  DigitaloceanLogo,
  DjangoLogo,
  ExpressLogo,
  GcloudLogo,
  GoLogo,
  NextLogo,
} from "@/components/logos";

type Node = { label: string; Logo: ComponentType<SVGProps<SVGSVGElement>>; ratio: number };

const frameworks: Node[] = [
  { label: "Next.js", Logo: NextLogo, ratio: 1 },
  { label: "Express", Logo: ExpressLogo, ratio: 1 },
  { label: "Django", Logo: DjangoLogo, ratio: 1 },
  { label: "Go", Logo: GoLogo, ratio: 207 / 78 },
];

const targets: Node[] = [
  { label: "AWS", Logo: AwsLogo, ratio: 304 / 182 },
  { label: "Google Cloud", Logo: GcloudLogo, ratio: 1 },
  { label: "DigitalOcean", Logo: DigitaloceanLogo, ratio: 1 },
];

const TOP = 50;
const TERMINAL = { x: 220, y: 150, w: 300, h: 110 };
const BOTTOM = 350;

function curve(x1: number, y1: number, x2: number, y2: number) {
  const my = (y1 + y2) / 2;
  return `M${x1} ${y1} C${x1} ${my}, ${x2} ${my}, ${x2} ${y2}`;
}

function Fitted({ node, cx, cy, box }: { node: Node; cx: number; cy: number; box: number }) {
  const w = node.ratio >= 1 ? box : box * node.ratio;
  const h = node.ratio >= 1 ? box / node.ratio : box;
  return <node.Logo x={cx - w / 2} y={cy - h / 2} width={w} height={h} />;
}

export function OverviewDiagram(props: SVGProps<SVGSVGElement>) {
  return (
    <svg
      viewBox="160 0 420 380"
      role="img"
      aria-label="Apps built with any framework are deployed by one command into your own cloud account"
      {...props}
    >
      <g fill="none" stroke="var(--steel)" strokeWidth="1.5" strokeDasharray="6 6" opacity="0.6">
        {[
          [190, TOP + 26, 290],
          [310, TOP + 26, 340],
          [430, TOP + 26, 400],
          [550, TOP + 12, 450],
        ].map(([x1, y1, x2]) => (
          <path key={x1} d={curve(x1, y1, x2, TERMINAL.y + 24)} />
        ))}
        {[
          [300, 190],
          [370, 370],
          [440, 550],
        ].map(([x1, x2]) => (
          <path key={x1} d={curve(x2, BOTTOM - 24, x1, TERMINAL.y + TERMINAL.h - 24)} />
        ))}
      </g>

      {frameworks.map((node, i) => (
        <g key={node.label}>
          <Fitted node={node} cx={190 + i * 120} cy={TOP} box={38} />
        </g>
      ))}

      <g>
        <rect
          x={TERMINAL.x}
          y={TERMINAL.y}
          width={TERMINAL.w}
          height={TERMINAL.h}
          fill="#16181a"
          stroke="#2a2d31"
        />
        <circle cx={TERMINAL.x + 16} cy={TERMINAL.y + 15} r="4" fill="#ff5f57" />
        <circle cx={TERMINAL.x + 32} cy={TERMINAL.y + 15} r="4" fill="#febc2e" />
        <circle cx={TERMINAL.x + 48} cy={TERMINAL.y + 15} r="4" fill="#28c840" />
        <line
          x1={TERMINAL.x}
          x2={TERMINAL.x + TERMINAL.w}
          y1={TERMINAL.y + 36}
          y2={TERMINAL.y + 36}
          stroke="#2a2d31"
        />
        <g className="font-mono text-[14px]">
          <text x={TERMINAL.x + 18} y={TERMINAL.y + 60} fill="#7d8590">
            $ <tspan fill="#f2f1ec">ocel deploy</tspan>
          </text>
          <text x={TERMINAL.x + 18} y={TERMINAL.y + 88} fill="#3ecf7a">
            ✓ <tspan fill="#9aa0a8">Deployed in 54s</tspan>
          </text>
        </g>
      </g>

      {targets.map((node, i) => (
        <g key={node.label}>
          <Fitted node={node} cx={190 + i * 180} cy={BOTTOM} box={38} />
        </g>
      ))}
    </svg>
  );
}

import type { ComponentType, SVGProps } from "react";
import {
  AwsLogo,
  DigitaloceanLogo,
  DjangoLogo,
  GcloudLogo,
  GoLogo,
  NextLogo,
  RustLogo,
} from "@/components/docs/logos";

// The docs' overview diagram (components/docs/overview-diagram.tsx), drawn for the landing:
// apps in any framework, one command, and every target, Ocel Cloud among them, each named.

type Node = {
  label: string;
  Logo: ComponentType<SVGProps<SVGSVGElement>>;
  ratio: number;
  /** The framework's docs page under content/docs/frameworks, for a framework node. */
  docs?: string;
};

function OcelCloudLogo(props: SVGProps<SVGSVGElement>) {
  return (
    <svg viewBox="0 0 100 100" aria-hidden="true" {...props}>
      <path
        fill="var(--electric)"
        d="M66.94 2.96 A50 50 0 1 0 98.62 38.33 L72.36 44.63 A23 23 0 1 1 57.79 28.36 Z"
      />
    </svg>
  );
}

const frameworks: Node[] = [
  { label: "Next.js", Logo: NextLogo, ratio: 1, docs: "nextjs" },
  // Django deploys as a Python app: ocel serves its wsgi.py or asgi.py entrypoint.
  { label: "Django", Logo: DjangoLogo, ratio: 1, docs: "python" },
  { label: "Go", Logo: GoLogo, ratio: 207 / 78, docs: "go" },
  { label: "Rust", Logo: RustLogo, ratio: 1, docs: "rust" },
];

const targets: Node[] = [
  { label: "AWS", Logo: AwsLogo, ratio: 304 / 182 },
  { label: "Google Cloud", Logo: GcloudLogo, ratio: 1 },
  { label: "Any VPS", Logo: DigitaloceanLogo, ratio: 1 },
  { label: "Ocel Cloud", Logo: OcelCloudLogo, ratio: 1 },
];

const XS = [190, 310, 430, 550];
const TOP = 50;
const TERMINAL = { x: 220, y: 210, w: 300, h: 110 };
const BOTTOM = 500;

function curve(x1: number, y1: number, x2: number, y2: number) {
  const my = (y1 + y2) / 2;
  return `M${x1} ${y1} C${x1} ${my}, ${x2} ${my}, ${x2} ${y2}`;
}

function Fitted({ node, cx, cy, box }: { node: Node; cx: number; cy: number; box: number }) {
  const w = node.ratio >= 1 ? box : box * node.ratio;
  const h = node.ratio >= 1 ? box / node.ratio : box;
  return <node.Logo x={cx - w / 2} y={cy - h / 2} width={w} height={h} />;
}

export function DeployDiagram(props: SVGProps<SVGSVGElement>) {
  const into = [290, 340, 400, 450];
  const outOf = [270, 340, 400, 470];
  return (
    <svg
      viewBox="140 0 460 560"
      role="img"
      aria-label="Apps in Next.js, Django, Go or Rust go through one ocel deploy command to AWS, Google Cloud, any VPS or Ocel Cloud"
      {...props}
    >
      <g fill="none" stroke="var(--steel)" strokeWidth="1.5" strokeDasharray="6 6" opacity="0.6">
        {XS.map((x, i) => (
          <path key={`in-${x}`} d={curve(x, TOP + 26, into[i] ?? x, TERMINAL.y + 24)} />
        ))}
        {XS.map((x, i) => (
          <path
            key={`out-${x}`}
            d={curve(outOf[i] ?? x, TERMINAL.y + TERMINAL.h - 24, x, BOTTOM - 30)}
          />
        ))}
      </g>

      {frameworks.map((node, i) => (
        <Fitted key={node.label} node={node} cx={XS[i] ?? 0} cy={TOP} box={36} />
      ))}

      <rect
        x={TERMINAL.x}
        y={TERMINAL.y}
        width={TERMINAL.w}
        height={TERMINAL.h}
        fill="#16181a"
        stroke="var(--foreground)"
        strokeWidth="1.5"
      />
      <circle cx={TERMINAL.x + 16} cy={TERMINAL.y + 15} r="4" fill="#ff5f57" />
      <circle cx={TERMINAL.x + 32} cy={TERMINAL.y + 15} r="4" fill="#febc2e" />
      <circle cx={TERMINAL.x + 48} cy={TERMINAL.y + 15} r="4" fill="#28c840" />
      <line
        x1={TERMINAL.x}
        x2={TERMINAL.x + TERMINAL.w}
        y1={TERMINAL.y + 30}
        y2={TERMINAL.y + 30}
        stroke="#2a2d31"
      />
      <g className="font-mono" fontSize="14">
        <text x={TERMINAL.x + 18} y={TERMINAL.y + 60} fill="#7d8590">
          $ <tspan fill="#f2f1ec">ocel deploy</tspan>
        </text>
        <text x={TERMINAL.x + 18} y={TERMINAL.y + 88} fill="#3ecf7a">
          ✓ <tspan fill="#9aa0a8">Deployed</tspan>
        </text>
      </g>

      {targets.map((node, i) => (
        <g key={node.label}>
          <Fitted node={node} cx={XS[i] ?? 0} cy={BOTTOM} box={36} />
          <text
            x={XS[i]}
            y={BOTTOM + 38}
            textAnchor="middle"
            className="font-mono"
            fontSize="11"
            fill="var(--muted-foreground)"
          >
            {node.label}
          </text>
        </g>
      ))}
    </svg>
  );
}

const symbols = {
  chevron: <path d="M6 3.5l4.5 4.5L6 12.5" />,
  folder: (
    <path d="M2 4.5A1.5 1.5 0 0 1 3.5 3h3l1.5 1.5h4.5A1.5 1.5 0 0 1 14 6v5.5a1.5 1.5 0 0 1-1.5 1.5h-9A1.5 1.5 0 0 1 2 11.5z" />
  ),
  environment: (
    <>
      <path d="M8 2.5l6 3-6 3-6-3z" />
      <path d="M2 8.5l6 3 6-3" />
      <path d="M2 11.5l6 3 6-3" />
    </>
  ),
  warning: (
    <>
      <path d="M8 2.5l6 11H2z" />
      <path d="M8 6.5v3" />
      <path d="M8 11.5v.5" />
    </>
  ),
  trash: (
    <>
      <path d="M3 4.5h10" />
      <path d="M6.5 4.5V3h3v1.5" />
      <path d="M4 4.5l.7 8.5h6.6l.7-8.5" />
    </>
  ),
  plus: <path d="M8 3v10M3 8h10" />,
  x: <path d="M4 4l8 8M12 4l-8 8" />,
  eye: (
    <>
      <path d="M1.5 8s2.5-4.5 6.5-4.5S14.5 8 14.5 8s-2.5 4.5-6.5 4.5S1.5 8 1.5 8z" />
      <circle cx="8" cy="8" r="2" />
    </>
  ),
  eyeOff: (
    <>
      <path d="M3 10.5C2 9.5 1.5 8 1.5 8s2.5-4.5 6.5-4.5c1 0 2 .3 2.8.7" />
      <path d="M13 5.5c1 1 1.5 2.5 1.5 2.5s-2.5 4.5-6.5 4.5c-1 0-2-.3-2.8-.7" />
      <path d="M2.5 13.5l11-11" />
    </>
  ),
  link: (
    <>
      <path d="M6.5 9.5l3-3" />
      <path d="M7 4.5l1-1a2.5 2.5 0 0 1 3.5 3.5l-1 1" />
      <path d="M9 11.5l-1 1a2.5 2.5 0 0 1-3.5-3.5l1-1" />
    </>
  ),
  info: (
    <>
      <circle cx="8" cy="8" r="6" />
      <path d="M8 7.5v3.5" />
      <path d="M8 5v.5" />
    </>
  ),
  file: (
    <>
      <path d="M4 2.5h5l3 3v8H4z" />
      <path d="M9 2.5v3h3" />
    </>
  ),
  copy: (
    <>
      <rect x="5.5" y="5.5" width="8" height="8" rx="1" />
      <path d="M10.5 5.5v-2a1 1 0 0 0-1-1h-6a1 1 0 0 0-1 1v6a1 1 0 0 0 1 1h2" />
    </>
  ),
};

export type IconName = keyof typeof symbols;

export function Sprite() {
  return (
    <svg style="display:none" aria-hidden="true">
      {Object.entries(symbols).map(([name, paths]) => (
        <symbol
          id={`i-${name}`}
          key={name}
          viewBox="0 0 16 16"
          fill="none"
          stroke="currentColor"
          stroke-width="1.5"
          stroke-linecap="round"
          stroke-linejoin="round"
        >
          {paths}
        </symbol>
      ))}
    </svg>
  );
}

export function Icon({ name }: { name: IconName }) {
  return (
    <svg class="icon" aria-hidden="true">
      <use href={`#i-${name}`} />
    </svg>
  );
}

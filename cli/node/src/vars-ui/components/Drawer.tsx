import { useEffect, useRef } from "preact/hooks";

import {
  addressKey,
  folderName,
  names,
  readersOf,
  referenceLine,
  sizeLine,
  variantAt,
  whenLine,
  type MatrixRow,
  type Variant,
} from "../model";
import {
  catalogue,
  closeDrawer,
  drawer,
  history,
  historyError,
  problems,
  state,
} from "../store";
import { Icon } from "./Icons";

export function Drawer() {
  const at = drawer.value;
  const panel = useRef<HTMLElement>(null);

  useEffect(() => {
    if (!at) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") closeDrawer();
    };
    const onPointer = (event: PointerEvent) => {
      const target = event.target;
      if (!(target instanceof Element)) return;
      if (panel.current?.contains(target)) return;
      if (target.closest("[aria-haspopup='dialog'],[role='menu']")) return;
      closeDrawer();
    };
    window.addEventListener("keydown", onKey);
    window.addEventListener("pointerdown", onPointer);
    return () => {
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("pointerdown", onPointer);
    };
  }, [at]);

  useEffect(() => {
    panel.current?.focus();
  }, [at]);

  if (!at) return null;
  const row = catalogue.value.rows.find((candidate) => candidate.key === at.key);
  const variant = variantAt(catalogue.value, at);
  if (!row || !variant) return null;
  const problem = problems.value.get(addressKey(at));

  return (
    <aside
      class="panel"
      role="dialog"
      aria-labelledby="drawer-title"
      tabIndex={-1}
      ref={panel}
    >
      <div class="panel-head">
        <div>
          <h2 id="drawer-title">{row.key}</h2>
          <Where variant={variant} />
        </div>
        <button
          type="button"
          class="iconbtn"
          title="Close"
          aria-label="close the details"
          onClick={closeDrawer}
        >
          <Icon name="x" />
        </button>
      </div>
      <div class="panel-body">
        <Facts row={row} variant={variant} />
        {variant.problem && (
          <p class="problem">The value here fails its schema: {variant.problem}</p>
        )}
        {problem && (
          <p class="problem">
            {problem.kind === "conflict"
              ? `This value changed since the page read it. Nothing was written here; decide again against what is there now: ${problem.message}`
              : problem.message}
          </p>
        )}
        <p class="eyebrow">History</p>
        <History />
      </div>
    </aside>
  );
}

function Where({ variant }: { variant: Variant }) {
  const { folder, environment } = variant.at;
  return (
    <div class="chips">
      <span class="chip">
        <Icon name={folder === "" ? "home" : "folder"} />
        {folderName(folder)}
      </span>
      <span class="chip chip-env">
        <Icon name="environment" />
        {environment === "" ? "base" : environment}
      </span>
      {variant.orphaned && (
        <span class="chip" data-tone="owed">
          <Icon name="warning" />
          orphaned
        </span>
      )}
    </div>
  );
}

function Facts({ row, variant }: { row: MatrixRow; variant: Variant }) {
  const apps = state.value?.matrix.apps ?? [];
  const readers = readersOf(row, apps);
  const scope = row.scope ?? [];
  return (
    <dl class="facts">
      <dt>Class</dt>
      <dd>{row.class}</dd>
      <dt>Scope</dt>
      <dd>
        {scope.length === 0
          ? "every folder; the root value unless a folder sets its own"
          : `only ${names(scope)}`}
      </dd>
      <dt>Here</dt>
      <dd>
        {variant.set
          ? `set · v${variant.version}`
          : variant.owed
            ? "required, not set"
            : "not set"}
      </dd>
      {variant.reference && (
        <>
          <dt>Source</dt>
          <dd>
            <span class="chip chip-ink">
              <Icon name="link" />
              {referenceLine(variant.reference)}
            </span>
            <p class="terminal">
              Live: edits there change what this project reads. Change the link with{" "}
              <code>ocel env ref {variant.at.key} …</code> in the terminal.
            </p>
          </dd>
        </>
      )}
      <dt>Read by</dt>
      <dd>
        {readers.length === 0 ? (
          <span class="muted">no app binds a folder that reads it</span>
        ) : (
          <span class="chips">
            {readers.map((app) => (
              <span
                class="chip"
                key={app.name}
                title={`${app.name} reads ${folderName(app.folder)}, then root`}
              >
                <Icon name="app" />
                {app.name}
              </span>
            ))}
          </span>
        )}
      </dd>
    </dl>
  );
}

function History() {
  if (historyError.value) {
    return <p class="problem">Could not read the history: {historyError.value}</p>;
  }
  const versions = history.value;
  if (versions === null) return <p class="muted">Reading…</p>;
  if (versions.length === 0) {
    return <p class="muted">No versions stored yet; nothing has been saved here.</p>;
  }
  return (
    <ul class="history">
      {versions.map((version) => (
        <li key={version.version}>
          <span class="version">v{version.version}</span>
          <span>{whenLine(version.createdAt)}</span>
          <span>{sizeLine(version.size)}</span>
        </li>
      ))}
    </ul>
  );
}

import { useEffect, useRef } from "preact/hooks";

import {
  addressKey,
  folderName,
  names,
  readersOf,
  referenceLine,
  sizeLine,
  whenLine,
  type KeyRow,
  type Variant,
} from "../model";
import {
  closeDrawer,
  drawer,
  history,
  historyError,
  state,
  tree,
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
      if (target.closest("[aria-haspopup='dialog']")) return;
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
  const row = tree.value.find((candidate) => candidate.key === at.key);
  const variant = [row?.root, ...(row?.children ?? [])].find(
    (candidate) => candidate && addressKey(candidate.at) === addressKey(at),
  );
  if (!row || !variant) return null;

  return (
    <aside
      class="drawer"
      role="dialog"
      aria-labelledby="drawer-title"
      tabIndex={-1}
      ref={panel}
    >
      <div class="drawer-head">
        <div>
          <h2 id="drawer-title">{row.key}</h2>
          <Where variant={variant} />
        </div>
        <button
          type="button"
          class="iconbtn"
          title="close"
          aria-label="close the details"
          onClick={closeDrawer}
        >
          <Icon name="x" />
        </button>
      </div>
      <Facts row={row} variant={variant} />
      {variant.problem && (
        <p class="problem">The value here fails its schema: {variant.problem}</p>
      )}
      <p class="eyebrow">History</p>
      <History />
    </aside>
  );
}

function Where({ variant }: { variant: Variant }) {
  const { folder, environment } = variant.at;
  if (folder === "" && environment === "") {
    return <p class="coordinate">root · every environment</p>;
  }
  return (
    <div class="badges">
      {folder !== "" && (
        <span class="badge" data-kind="folder">
          <Icon name="folder" />
          {folder}
        </span>
      )}
      {environment !== "" && (
        <span
          class="badge"
          data-kind="environment"
          data-tone={variant.orphaned ? "owed" : undefined}
        >
          <Icon name="environment" />
          {environment}
        </span>
      )}
      {variant.orphaned && (
        <span class="badge" data-tone="owed">
          <Icon name="warning" />
          orphaned
        </span>
      )}
    </div>
  );
}

function Facts({ row, variant }: { row: KeyRow; variant: Variant }) {
  const apps = state.value?.matrix.apps ?? [];
  const readers = readersOf(row, apps);
  return (
    <dl class="facts">
      <dt>class</dt>
      <dd>{row.class}</dd>
      <dt>scope</dt>
      <dd>
        {row.scope.length === 0
          ? "every folder — the root value unless a folder sets its own"
          : `only ${names(row.scope)}`}
      </dd>
      <dt>here</dt>
      <dd>
        {variant.set
          ? `set · v${variant.version}`
          : variant.owed
            ? "required, not set"
            : "not set"}
      </dd>
      {variant.reference && (
        <>
          <dt>source</dt>
          <dd>
            <span class="badge" data-kind="reference">
              <Icon name="link" />
              {referenceLine(variant.reference)}
            </span>
            <span class="terminal">
              {" "}
              live — edits there change what this project reads; change the link with{" "}
              <code>ocel env ref</code>
            </span>
          </dd>
        </>
      )}
      <dt>read by</dt>
      <dd>
        {readers.length === 0 ? (
          <span class="unset">no app binds a folder that reads it</span>
        ) : (
          <span class="readers">
            {readers.map((app) => (
              <span class="badge" key={app.name} title={`${app.name} reads ${folderName(app.folder)}, then root`}>
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
  if (versions === null) return <p class="empty">Reading…</p>;
  if (versions.length === 0) {
    return <p class="empty">No versions stored yet — nothing has been saved here.</p>;
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

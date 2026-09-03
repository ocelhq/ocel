import { useEffect, useRef } from "preact/hooks";

import { addressKey, plural, type Address, type CopyCell } from "../model";
import {
  baselines,
  closeCopy,
  confirmCopy,
  copying,
  state,
  toggleCopy,
} from "../store";
import { Icon } from "./Icons";

export function CopyDialog() {
  const dialog = copying.value;
  const panel = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!dialog) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") closeCopy();
    };
    window.addEventListener("keydown", onKey);
    panel.current?.focus();
    return () => window.removeEventListener("keydown", onKey);
  }, [dialog !== null]);

  if (!dialog) return null;
  const { plan, chosen, busy } = dialog;
  const here = state.value?.tier ?? "";
  const count = [...plan.fills, ...plan.overwrites].filter((cell) =>
    chosen.has(addressKey(cell.at)),
  ).length;
  const nothing =
    plan.fills.length + plan.overwrites.length + plan.unreadable.length + plan.skipped.length === 0;

  return (
    <div class="scrim" onPointerDown={(event) => event.target === event.currentTarget && closeCopy()}>
      <div
        class="dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="copy-title"
        tabIndex={-1}
        ref={panel}
      >
        <div class="drawer-head">
          <div>
            <h2 id="copy-title">Copy from {dialog.tier}</h2>
            <p class="coordinate">
              a one-time copy into {here} — nothing stays linked
            </p>
          </div>
          <button
            type="button"
            class="iconbtn"
            title="close"
            aria-label="close without copying"
            disabled={busy}
            onClick={closeCopy}
          >
            <Icon name="x" />
          </button>
        </div>

        {nothing && (
          <p class="empty">{dialog.tier} holds no value for a key this project declares.</p>
        )}

        <Group
          title="fills an empty cell here"
          cells={plan.fills}
          chosen={chosen}
          busy={busy}
        />
        <Group
          title="overwrites a value here"
          hint="unchecked unless you say so"
          cells={plan.overwrites}
          chosen={chosen}
          busy={busy}
        />

        {plan.unreadable.length > 0 && (
          <>
            <p class="eyebrow">could not be read from {dialog.tier}</p>
            <ul class="diff">
              {plan.unreadable.map((cell) => (
                <li key={addressKey(cell.at)} class="diff-row" data-tone="owed">
                  <Where at={cell.at} />
                  <span class="fault">
                    <Icon name="warning" />
                    {cell.error}
                  </span>
                </li>
              ))}
            </ul>
          </>
        )}

        {plan.skipped.length > 0 && (
          <>
            <p class="eyebrow">left alone</p>
            <ul class="diff">
              {plan.skipped.map((skip, index) => (
                <li key={index} class="diff-row">
                  <span class="key">{skip.key}</span>
                  <span class="unset">{skip.reason}</span>
                </li>
              ))}
            </ul>
          </>
        )}

        {dialog.error && <p class="problem">{dialog.error}</p>}

        <div class="dialog-actions">
          <button
            type="button"
            class="primary"
            disabled={busy || count === 0}
            onClick={() => void confirmCopy()}
          >
            {busy ? "Copying…" : count === 0 ? "Nothing chosen" : `Copy ${plural(count, "value")}`}
          </button>
          <button type="button" class="linkish" disabled={busy} onClick={closeCopy}>
            cancel
          </button>
        </div>
      </div>
    </div>
  );
}

function Group({
  title,
  hint,
  cells,
  chosen,
  busy,
}: {
  title: string;
  hint?: string;
  cells: CopyCell[];
  chosen: ReadonlySet<string>;
  busy: boolean;
}) {
  if (cells.length === 0) return null;
  return (
    <>
      <p class="eyebrow">
        {title} ({cells.length})
        {hint && <span class="axis"> — {hint}</span>}
      </p>
      <ul class="diff">
        {cells.map((cell) => {
          const key = addressKey(cell.at);
          return (
            <li key={key} class="diff-row">
              <label class="diff-pick">
                <input
                  type="checkbox"
                  checked={chosen.has(key)}
                  disabled={busy}
                  onChange={() => toggleCopy(cell.at)}
                />
                <Where at={cell.at} />
              </label>
              <span class="diff-values">
                <Here cell={cell} />
                <span class="arrow" aria-hidden="true">
                  →
                </span>
                <There cell={cell} />
              </span>
            </li>
          );
        })}
      </ul>
    </>
  );
}

function Where({ at }: { at: Address }) {
  return (
    <span class="diff-where">
      <span class="key">{at.key}</span>
      {at.folder !== "" && (
        <span class="badge" data-kind="folder">
          <Icon name="folder" />
          {at.folder}
        </span>
      )}
      {at.environment !== "" && (
        <span class="badge" data-kind="environment">
          <Icon name="environment" />
          {at.environment}
        </span>
      )}
    </span>
  );
}

function Here({ cell }: { cell: CopyCell }) {
  if (!cell.hereSet) return <span class="unset">empty</span>;
  if (cell.class === "secret") {
    return <span class="stored">secret · v{cell.hereVersion}</span>;
  }
  const revealed = baselines.value.get(addressKey(cell.at));
  return (
    <span class="stored" title={`v${cell.hereVersion}`}>
      {revealed ?? "••••••••"}
    </span>
  );
}

function There({ cell }: { cell: CopyCell }) {
  if (cell.class === "secret") {
    return <span class="unset">secret — copied without showing it</span>;
  }
  return <span class="there">{cell.there}</span>;
}

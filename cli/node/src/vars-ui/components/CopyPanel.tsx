import { useEffect, useRef } from "preact/hooks";

import {
  addressKey,
  copyTree,
  folderName,
  plural,
  type Address,
  type CopyBranch,
  type CopyCell,
} from "../model";
import {
  baselines,
  closeCopy,
  confirmCopy,
  copying,
  state,
  toggleBranch,
  toggleCopy,
  toggleOverwriting,
} from "../store";
import { Icon } from "./Icons";

export function CopyPanel() {
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
  const branches = copyTree(plan);
  const nothing =
    branches.length + plan.unreadable.length + plan.skipped.length === 0;

  return (
    <div class="scrim" onPointerDown={(event) => event.target === event.currentTarget && closeCopy()}>
      <div
        class="panel"
        role="dialog"
        aria-modal="true"
        aria-labelledby="copy-title"
        tabIndex={-1}
        ref={panel}
      >
        <div class="panel-head">
          <div>
            <h2 id="copy-title">Copy from {dialog.tier}</h2>
            <p class="muted">A one-time copy into {here}. Nothing stays linked.</p>
          </div>
          <button
            type="button"
            class="iconbtn"
            title="Close"
            aria-label="close without copying"
            disabled={busy}
            onClick={closeCopy}
          >
            <Icon name="x" />
          </button>
        </div>

        <div class="panel-body">
          <div class="route">
            <div class="route-end">
              <span class="eyebrow">Source</span>
              <span class="route-name">{dialog.tier}</span>
            </div>
            <span class="route-arrow" aria-hidden="true">
              →
            </span>
            <div class="route-end" data-here="true">
              <span class="eyebrow">Destination</span>
              <span class="route-name">{here}</span>
            </div>
          </div>

          {nothing ? (
            <p class="muted">{dialog.tier} holds no value for a key this project declares.</p>
          ) : (
            <>
              <p class="eyebrow">Values to copy</p>
              <label class="switch">
                <input
                  type="checkbox"
                  checked={dialog.overwriting}
                  disabled={busy || plan.overwrites.length === 0}
                  onChange={toggleOverwriting}
                />
                Overwrite values already set here
                {plan.overwrites.length > 0 && (
                  <span class="muted"> ({plan.overwrites.length})</span>
                )}
              </label>
              <ul class="tree">
                {branches.map((branch) => (
                  <Branch key={branch.folder} branch={branch} />
                ))}
              </ul>
            </>
          )}

          {plan.unreadable.length > 0 && (
            <>
              <p class="eyebrow">Could not be read from {dialog.tier}</p>
              <ul class="plain-list">
                {plan.unreadable.map((cell) => (
                  <li key={addressKey(cell.at)}>
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
              <p class="eyebrow">Left alone</p>
              <ul class="plain-list">
                {plan.skipped.map((skip, index) => (
                  <li key={index}>
                    <span class="key">{skip.key}</span>
                    <span class="muted">{skip.reason}</span>
                  </li>
                ))}
              </ul>
            </>
          )}

          {dialog.error && <p class="problem">{dialog.error}</p>}
        </div>

        <div class="panel-foot">
          <span class="muted">{plural(count, "value")} chosen</span>
          <span class="bulk-gap" />
          <button type="button" class="btn" disabled={busy} onClick={closeCopy}>
            Cancel
          </button>
          <button
            type="button"
            class="btn btn-primary"
            disabled={busy || count === 0}
            onClick={() => void confirmCopy()}
          >
            <Icon name="copy" />
            {busy ? "Copying…" : `Copy ${plural(count, "value")}`}
          </button>
        </div>
      </div>
    </div>
  );
}

function Branch({ branch }: { branch: CopyBranch }) {
  const dialog = copying.value!;
  const open = dialog.open.has(branch.folder);
  const enabled = branch.cells.filter((cell) => !cell.hereSet || dialog.overwriting);
  const picked = enabled.filter((cell) => dialog.chosen.has(addressKey(cell.at)));
  const all = enabled.length > 0 && picked.length === enabled.length;
  return (
    <li class="tree-branch" data-open={open}>
      <div class="tree-row">
        <button
          type="button"
          class="iconbtn tree-toggle"
          aria-expanded={open}
          aria-label={`${open ? "collapse" : "expand"} ${folderName(branch.folder)}`}
          onClick={() => toggleBranch(branch.folder)}
        >
          <Icon name="chevron" />
        </button>
        <label class="tree-pick">
          <input
            type="checkbox"
            checked={all}
            disabled={dialog.busy || enabled.length === 0}
            onChange={(event) =>
              toggleCopy(
                enabled.map((cell) => cell.at),
                event.currentTarget.checked,
              )
            }
          />
          <Icon name={branch.folder === "" ? "home" : "folder"} />
          <span class="key">{branch.folder === "" ? "/" : branch.folder.slice(1)}</span>
        </label>
        <span class="muted">{plural(branch.cells.length, "value")}</span>
      </div>
      {open && (
        <ul class="tree-leaves">
          {branch.cells.map((cell) => (
            <Leaf key={addressKey(cell.at)} cell={cell} />
          ))}
        </ul>
      )}
    </li>
  );
}

function Leaf({ cell }: { cell: CopyCell }) {
  const dialog = copying.value!;
  const key = addressKey(cell.at);
  const locked = cell.hereSet && !dialog.overwriting;
  return (
    <li class="tree-leaf" data-locked={locked}>
      <label class="tree-pick">
        <input
          type="checkbox"
          checked={dialog.chosen.has(key)}
          disabled={dialog.busy || locked}
          onChange={() => toggleCopy([cell.at])}
        />
        <Icon name={cell.class === "secret" ? "lock" : "key"} />
        <span class="key">{cell.at.key}</span>
        {cell.at.environment !== "" && (
          <span class="chip chip-env">
            <Icon name="environment" />
            {cell.at.environment}
          </span>
        )}
        {cell.hereSet && <span class="chip">already set</span>}
      </label>
      <span class="tree-values">
        <Here cell={cell} />
        <span class="route-arrow" aria-hidden="true">
          →
        </span>
        <There cell={cell} />
      </span>
    </li>
  );
}

function Where({ at }: { at: Address }) {
  return (
    <span class="chips">
      <span class="key">{at.key}</span>
      <span class="chip">
        <Icon name={at.folder === "" ? "home" : "folder"} />
        {folderName(at.folder)}
      </span>
      {at.environment !== "" && (
        <span class="chip chip-env">
          <Icon name="environment" />
          {at.environment}
        </span>
      )}
    </span>
  );
}

function Here({ cell }: { cell: CopyCell }) {
  if (!cell.hereSet) return <span class="muted">empty</span>;
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
    return <span class="muted">secret, copied without showing it</span>;
  }
  return <span class="there">{cell.there}</span>;
}

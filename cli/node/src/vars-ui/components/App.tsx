import { folderName, names, plural } from "../model";
import {
  abandon,
  askRemoval,
  cancelRemoval,
  clearSelection,
  confirmRemoval,
  dirty,
  discard,
  dismissDrop,
  dropped,
  farewell,
  finishError,
  finishing,
  hideSelected,
  outcome,
  owedOnly,
  removing,
  resume,
  revealSelected,
  save,
  saving,
  selected,
  showOwed,
  state,
  unfilled,
  variants,
} from "../store";
import { Apps } from "./Apps";
import { CopyPanel } from "./CopyPanel";
import { Drawer } from "./Drawer";
import { Icon, Sprite } from "./Icons";
import { Masthead } from "./Masthead";
import { Table } from "./Table";

function DropNotice() {
  const drop = dropped.value;
  if (!drop) return null;
  const where = folderName(drop.folder);
  return (
    <div class="notice" role="status">
      <Icon name="file" />
      <div class="notice-body">
        <p>
          <strong>{drop.name}</strong>:{" "}
          {drop.fills.length === 0
            ? `nothing to fill in ${where}.`
            : `${plural(drop.fills.length, "row")} filled in ${where}, unsaved until you save.`}
        </p>
        {drop.undeclared.length > 0 && (
          <p>
            Ignored {plural(drop.undeclared.length, "key")} this project does not
            declare: <code>{names(drop.undeclared)}</code>. Keys come from{" "}
            <code>defineEnv</code> in app code; this page cannot create one.
          </p>
        )}
        {drop.skipped.map((skip) => (
          <p key={skip.key}>Skipped {skip.key}: {skip.reason}.</p>
        ))}
      </div>
      <button
        type="button"
        class="iconbtn"
        title="Dismiss"
        aria-label="dismiss this notice"
        onClick={dismissDrop}
      >
        <Icon name="x" />
      </button>
    </div>
  );
}

function Banner() {
  const current = state.value;
  const recovery = current?.recovery;
  if (!current || !recovery) return null;
  const left = unfilled.value.length;
  return (
    <div class="banner" role="status">
      <Icon name="warning" />
      <p>
        Deploy <code>{recovery.deploy}</code> is waiting on{" "}
        {plural(recovery.owed.length, "cell")} it was refused.{" "}
        {left === 0
          ? "Every one now holds a value; save and resume below."
          : `${plural(left, "cell")} still ${left === 1 ? "needs" : "need"} a value.`}
      </p>
      <label class="switch">
        <input
          type="checkbox"
          checked={owedOnly.value}
          onChange={(event) => showOwed(event.currentTarget.checked)}
        />
        Show only what the deploy needs
      </label>
    </div>
  );
}

function BulkBar() {
  const picked = selected.value;
  if (picked.size === 0) return null;
  const cells = [...picked]
    .map((key) => variants.value.get(key))
    .filter((v) => v !== undefined)
    .map((v) => v!.at);
  const removable = cells.filter((at) => {
    const v = variants.value.get(`${at.key} ${at.folder} ${at.environment}`);
    return v !== undefined && v.set && !v.reference;
  });
  return (
    <div class="bulk" role="toolbar" aria-label="selected rows">
      <span class="bulk-count">{picked.size} selected</span>
      <button type="button" class="btn btn-ghost btn-small" onClick={clearSelection}>
        Unselect all
      </button>
      <span class="bulk-gap" />
      <button type="button" class="btn btn-small" onClick={() => void revealSelected()}>
        <Icon name="eye" />
        Reveal
      </button>
      <button type="button" class="btn btn-small" onClick={hideSelected}>
        <Icon name="eyeOff" />
        Hide
      </button>
      <button
        type="button"
        class="btn btn-small btn-danger"
        disabled={removable.length === 0 || saving.value}
        onClick={() => askRemoval(removable)}
      >
        <Icon name="trash" />
        Remove {removable.length > 0 ? plural(removable.length, "value") : "values"}
      </button>
    </div>
  );
}

function Confirm() {
  const asked = removing.value;
  if (!asked) return null;
  const busy = saving.value;
  return (
    <div class="scrim" onPointerDown={(event) => event.target === event.currentTarget && cancelRemoval()}>
      <div class="dialog" role="alertdialog" aria-modal="true" aria-labelledby="remove-title">
        <h2 id="remove-title">Remove {plural(asked.cells.length, "value")}?</h2>
        <p class="muted">
          The stored value goes away for {names(asked.cells.map((cell) => cell.at.key))}. History
          keeps the versions; nothing else on this page is touched.
        </p>
        <div class="dialog-actions">
          <button type="button" class="btn btn-danger" disabled={busy} onClick={() => void confirmRemoval()}>
            <Icon name="trash" />
            {busy ? "Removing…" : "Remove"}
          </button>
          <button type="button" class="btn" disabled={busy} onClick={cancelRemoval}>
            Cancel
          </button>
        </div>
      </div>
    </div>
  );
}

function Bar({ recovery }: { recovery: boolean }) {
  const pending = dirty.value.length;
  const busy = saving.value || finishing.value;
  const said = [outcome.value?.text, finishError.value].filter(Boolean).join(" ");
  if (!recovery && pending === 0 && said === "") return null;
  const left = unfilled.value.length;
  return (
    <footer class="bar" data-recovery={recovery}>
      <div class="bar-actions">
        {pending > 0 && (
          <>
            <span class="bar-count">{plural(pending, "unsaved change")}</span>
            <button
              type="button"
              class={recovery ? "btn" : "btn btn-primary"}
              disabled={busy}
              onClick={() => void save()}
            >
              {saving.value ? "Saving…" : "Save"}
            </button>
            <button type="button" class="btn btn-ghost" disabled={busy} onClick={discard}>
              Discard
            </button>
          </>
        )}
        <p
          class="outcome"
          aria-live="polite"
          data-tone={outcome.value?.tone ?? (finishError.value ? "owed" : undefined)}
        >
          {said}
        </p>
      </div>
      {recovery && (
        <div class="finish">
          <button
            type="button"
            class="btn btn-primary"
            disabled={busy || left > 0}
            title={
              left > 0
                ? `${plural(left, "cell")} the deploy needs ${left === 1 ? "is" : "are"} still empty or invalid`
                : undefined
            }
            onClick={() => void resume()}
          >
            {finishing.value ? "Resuming…" : "Save and resume the deploy"}
          </button>
          <button type="button" class="btn btn-ghost btn-danger-text" disabled={busy} onClick={() => void abandon()}>
            Abandon the deploy
          </button>
        </div>
      )}
    </footer>
  );
}

export function App() {
  if (farewell.value !== null) {
    return <p class="farewell">{farewell.value}</p>;
  }
  const current = state.value;
  if (!current) {
    return <p class="loading">Reading this project’s variables…</p>;
  }
  const recovery = current.recovery !== undefined;
  return (
    <div class="frame">
      <Sprite />
      <div class="sheet">
        <Masthead current={current} />
        <Banner />
        <Apps current={current} />
        <DropNotice />
        <Table />
      </div>
      <BulkBar />
      <Bar recovery={recovery} />
      <Drawer />
      <CopyPanel />
      <Confirm />
    </div>
  );
}

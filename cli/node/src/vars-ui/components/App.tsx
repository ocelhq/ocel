import { doneLabel, folderName, names, owedCount, plural } from "../model";
import {
  abandon,
  collapseAll,
  copyLoading,
  dirty,
  discard,
  dismissDrop,
  dropped,
  expandAll,
  farewell,
  finishError,
  finishing,
  hideAll,
  leave,
  leaveDiscarding,
  openCopy,
  outcome,
  resume,
  revealAll,
  save,
  saving,
  state,
  unfilled,
} from "../store";
import { Apps } from "./Apps";
import { CopyDialog } from "./CopyDialog";
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
      <div class="notice-body">
        <p>
          <Icon name="file" /> {drop.name}:{" "}
          {drop.fills.length === 0
            ? `nothing to fill in ${where}.`
            : `${plural(drop.fills.length, "row")} filled in ${where} — unsaved until you save.`}
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
        title="dismiss"
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
        Deploy <code>{recovery.deploy}</code> of {current.slug} · {current.tier}{" "}
        is waiting on {plural(recovery.owed.length, "cell")} it was refused.{" "}
        {left === 0
          ? "Every one now holds a value; save and resume below."
          : `${plural(left, "cell")} still ${left === 1 ? "needs" : "need"} a value — they sit at the top.`}
      </p>
    </div>
  );
}

function Finish({ recovery }: { recovery: boolean }) {
  const pending = dirty.value.length;
  const busy = saving.value || finishing.value;
  if (!recovery) {
    return pending > 0 ? (
      <button type="button" class="done" disabled={busy} onClick={leaveDiscarding}>
        Return without saving
      </button>
    ) : (
      <button type="button" class="done" disabled={busy} onClick={leave}>
        {doneLabel(owedCount(state.value!))}
      </button>
    );
  }
  const left = unfilled.value.length;
  return (
    <div class="finish">
      <button
        type="button"
        class="primary"
        disabled={busy || left > 0}
        title={left > 0 ? `${plural(left, "cell")} the deploy needs ${left === 1 ? "is" : "are"} still empty or invalid` : undefined}
        onClick={() => void resume()}
      >
        {finishing.value ? "Resuming…" : "Save and resume the deploy"}
      </button>
      <button type="button" class="linkish abandon" disabled={busy} onClick={() => void abandon()}>
        Abandon the deploy — this fails the deploy
      </button>
    </div>
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
  const pending = dirty.value.length;
  const busy = saving.value || finishing.value;
  const recovery = current.recovery !== undefined;
  return (
    <div class="frame">
      <Sprite />
      <div class="sheet">
        <Masthead current={current} />
        <Banner />
        <p class="eyebrow">
          Apps{" "}
          <span class="axis">— each reads its own folder, then the root</span>
        </p>
        <Apps current={current} />
        <div class="toolbar">
          <p class="eyebrow">
            Variables{" "}
            <span class="axis">
              — one row per key; folder and environment variants nest under it
            </span>
          </p>
          <div class="tools">
            <button type="button" class="linkish" onClick={expandAll}>
              expand all
            </button>
            <button type="button" class="linkish" onClick={collapseAll}>
              collapse all
            </button>
            <span class="tools-gap" />
            <button type="button" class="linkish" onClick={() => void revealAll()}>
              <Icon name="eye" /> reveal all
            </button>
            <button type="button" class="linkish" onClick={hideAll}>
              hide all
            </button>
            <span class="tools-gap" />
            <button
              type="button"
              class="linkish"
              disabled={copyLoading.value}
              onClick={() => void openCopy()}
            >
              <Icon name="copy" />{" "}
              {copyLoading.value ? "reading…" : `copy from ${current.other}`}
            </button>
          </div>
        </div>
        <DropNotice />
        <Table />
      </div>
      <Drawer />
      <CopyDialog />
      <footer class="bar" data-recovery={recovery}>
        <div class="bar-actions">
          {!recovery && (
            <button
              type="button"
              class="primary"
              disabled={busy || pending === 0}
              onClick={() => void save()}
            >
              {saving.value
                ? "Saving…"
                : pending === 0
                  ? "Nothing to save"
                  : `Save ${plural(pending, "change")}`}
            </button>
          )}
          {recovery && pending > 0 && (
            <button
              type="button"
              class="done"
              disabled={busy}
              onClick={() => void save()}
            >
              {saving.value ? "Saving…" : `Save ${plural(pending, "change")}`}
            </button>
          )}
          {pending > 0 && (
            <button type="button" class="linkish" disabled={busy} onClick={discard}>
              discard changes
            </button>
          )}
          <p
            class="outcome"
            aria-live="polite"
            data-tone={outcome.value?.tone ?? (finishError.value ? "owed" : undefined)}
          >
            {[outcome.value?.text, finishError.value].filter(Boolean).join(" ")}
          </p>
        </div>
        <Finish recovery={recovery} />
      </footer>
    </div>
  );
}

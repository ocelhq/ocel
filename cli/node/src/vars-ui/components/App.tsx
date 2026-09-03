import { doneLabel, folderName, names, owedCount, plural } from "../model";
import {
  collapseAll,
  dirty,
  discard,
  dismissDrop,
  dropped,
  expandAll,
  farewell,
  hideAll,
  leave,
  outcome,
  revealAll,
  save,
  saving,
  state,
} from "../store";
import { Apps } from "./Apps";
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

export function App() {
  if (farewell.value !== null) {
    return <p class="farewell">{farewell.value}</p>;
  }
  const current = state.value;
  if (!current) {
    return <p class="loading">Reading this project’s variables…</p>;
  }
  const pending = dirty.value.length;
  const busy = saving.value;
  return (
    <div class="frame">
      <Sprite />
      <div class="sheet">
        <Masthead current={current} />
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
          </div>
        </div>
        <DropNotice />
        <Table />
      </div>
      <Drawer />
      <footer class="bar">
        <div class="bar-actions">
          <button
            type="button"
            class="primary"
            disabled={busy || pending === 0}
            onClick={() => void save()}
          >
            {busy
              ? "Saving…"
              : pending === 0
                ? "Nothing to save"
                : `Save ${plural(pending, "change")}`}
          </button>
          {pending > 0 && (
            <button type="button" class="linkish" disabled={busy} onClick={discard}>
              discard changes
            </button>
          )}
          <p
            class="outcome"
            aria-live="polite"
            data-tone={outcome.value?.tone}
          >
            {outcome.value?.text}
          </p>
        </div>
        <button type="button" class="done" disabled={busy} onClick={leave}>
          {doneLabel(owedCount(current))}
        </button>
      </footer>
    </div>
  );
}

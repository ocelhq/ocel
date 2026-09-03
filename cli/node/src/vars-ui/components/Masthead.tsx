import { doneLabel, owedCount, tallyLine, type State } from "../model";
import { dirty, finishing, leave, leaveDiscarding, saving } from "../store";
import { Icon } from "./Icons";

export function Masthead({ current }: { current: State }) {
  const owed = owedCount(current);
  const recovery = current.recovery !== undefined;
  const pending = dirty.value.length;
  const busy = saving.value || finishing.value;
  return (
    <header class="masthead">
      <div>
        <h1 class="slug">
          {current.slug} <span class="tier">· {current.tier}</span>
        </h1>
        <p class="tally" data-owed={owed}>
          {owed === 0 && <Icon name="check" />}
          {tallyLine(owed)}
        </p>
      </div>
      {!recovery &&
        (pending > 0 ? (
          <button type="button" class="btn" disabled={busy} onClick={leaveDiscarding}>
            Return without saving
          </button>
        ) : (
          <button type="button" class="btn" disabled={busy} onClick={leave}>
            {doneLabel(owed)}
          </button>
        ))}
    </header>
  );
}

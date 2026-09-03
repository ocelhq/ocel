import { owedCount, tallyLine, type State } from "../model";

export function Masthead({ current }: { current: State }) {
  const owed = owedCount(current);
  return (
    <header class="masthead">
      <h1 class="slug">
        {current.slug} <span class="tier">· {current.tier}</span>
      </h1>
      <p class="tally" data-owed={owed}>
        {tallyLine(owed)}
      </p>
    </header>
  );
}

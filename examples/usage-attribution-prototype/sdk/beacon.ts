export const PREFIX = "@@OCEL-BEACON@@";

export function beacon(record: object) {
  process.stdout.write("\n" + PREFIX + JSON.stringify(record) + "\n");
}

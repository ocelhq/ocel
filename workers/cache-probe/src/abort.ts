// Every gate in the race runner is fatal rather than a warning, because each of
// them turns a green run into a fabrication that looks like an answer. This is
// the type that says so: the runner's trial loop discards a plain Error as a
// transport failure and re-throws an Abort, so anything that means "the
// instrument is broken, not the cache" must be one of these.
export class Abort extends Error {}

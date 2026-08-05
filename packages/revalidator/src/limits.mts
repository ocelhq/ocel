// The sizing contract this package hands cloud/aws/bootstrap.
//
// Every number here is a resource property somewhere else — a function timeout,
// a queue's visibility timeout, an event source mapping's batch size — and the
// arithmetic BETWEEN them is the part that breaks silently when one of them
// moves. It is asserted in test/limits.test.mts rather than left in prose.

// One render's budget at the origin. A HEAD runs the whole render pipeline.
export const triggerTimeoutMs = 10_000;

// One deploy record read from S3, memoized per isrPrefix for the invocation.
export const originTimeoutMs = 2_000;

// The event source mapping's batch size.
export const batchSize = 10;

// The function timeout. The worst case is a full batch of records that share no
// isrPrefix, so every record pays both budgets and nothing is memoized; the
// remainder is cold start, signing, and the SDK-free JSON work between them.
export const functionTimeoutMs = 150_000;

// The queue's visibility timeout. It must outlast a function that runs all the
// way to its own timeout, or the queue redelivers records that were already
// processed successfully.
export const visibilityTimeoutMs = 300_000;

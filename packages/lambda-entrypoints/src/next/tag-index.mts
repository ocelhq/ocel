// The tag write lives in @ocel/next-cache, where the Cloudflare worker can reach
// it without pulling this package's Node and AWS SDK graph into a workerd
// bundle. Both tiers write the same rows, so there can only be one builder of
// them; this module is what keeps the Lambda's import sites pointed at it.
export {
  isGuardRejection,
  tagRecordUpdate,
  tagSortKey,
  type TagRecordUpdate,
} from "@ocel/next-cache";

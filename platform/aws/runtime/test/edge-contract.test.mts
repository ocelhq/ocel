import { tagSnapshotKey } from "@framework/next-cache";
import contract from "@framework/next-cache/fixtures/edge-contract.json" with { type: "json" };
import { edgeCacheHeader, edgeCacheTagHeader } from "@platform/edge-contract/edge-cache";
import { purgeHeader } from "@platform/edge-contract/purge";
import { parseStorageCoordinate } from "@platform/edge-contract/storage-key";
import { expect, test } from "vitest";
import { parseTagNamespace, tagNamespace } from "../src/cache/tag-index.mjs";
import { edgeReadableTagging } from "../src/next/object-store.mjs";

test("the snapshot is keyed where the deploy seeds it", () => {
  expect(tagSnapshotKey(contract.tagNamespace.coordinate)).toBe(contract.tagSnapshotKey);
});

test("tag records are namespaced exactly as the deploy grants them", () => {
  expect(tagNamespace(contract.tagNamespace.coordinate)).toBe(
    contract.tagNamespace.partitionKeyPrefix,
  );
});

test("the publisher reads back the very release the deploy wrote the tag items for", () => {
  expect(parseTagNamespace(contract.tagNamespace.partitionKeyPrefix)).toEqual(
    contract.tagNamespace.coordinate,
  );
});

test("the contract's storage coordinate and key are one coordinate, spelled twice", () => {
  const { project, env, app, release } = contract.tagNamespace.coordinate;
  expect(parseStorageCoordinate(contract.tagNamespace.storageCoordinate)).toEqual(
    contract.tagNamespace.coordinate,
  );
  expect(contract.tagNamespace.partitionKeyPrefix).toBe(
    `PROJECT#${project}#STACK#${env}--${app}--${release}#TAG#`,
  );
});

test("a purge is announced under the header name the edge reads", () => {
  expect(purgeHeader).toBe(contract.purgeHeader);
});

test("the origin shapes for an edge cache under the header name the edge sends", () => {
  expect(edgeCacheHeader).toBe(contract.edgeCacheHeader);
});

test("the origin names an edge-cached page's tags under the header name the edge reads", () => {
  expect(edgeCacheTagHeader).toBe(contract.edgeCacheTagHeader);
});

test("page entries are tagged edge-readable exactly as the deploy tags what the edge may read", () => {
  expect(edgeReadableTagging).toBe(contract.edgeReadableTagging);
});

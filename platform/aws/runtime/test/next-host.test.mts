import { expect, test } from "vitest";
import { newAwsNextHost } from "../src/next/next-host.mjs";

test("the AWS host declares Lambda's task root as the function directory", () => {
  expect(newAwsNextHost({ LAMBDA_TASK_ROOT: "/var/task" }).functionDir).toBe("/var/task");
});

test("the AWS host's instance cache holds a tenth of Lambda's configured memory", () => {
  const cache = newAwsNextHost({ AWS_LAMBDA_FUNCTION_MEMORY_SIZE: "1" }).instanceCache!;

  cache.write("fits", 1, 100 * 1024);
  cache.write("over", 2, 100 * 1024);
  cache.write("huge", 3, 110 * 1024);

  expect(cache.read("fits")).toBeUndefined();
  expect(cache.read("over")).toBe(2);
  expect(cache.read("huge")).toBeUndefined();
});

test("the AWS host's instance cache holds 50 MiB when Lambda names no memory", () => {
  const cache = newAwsNextHost({}).instanceCache!;

  cache.write("fits", 1, 50 * 1024 * 1024);
  cache.write("huge", 2, 50 * 1024 * 1024 + 1);

  expect(cache.read("fits")).toBe(1);
  expect(cache.read("huge")).toBeUndefined();
});

test("the AWS host declares the 50 cache tags CloudFront stores per object", () => {
  expect(newAwsNextHost({}).cacheTagsPerObject).toBe(50);
});

process.env.VIPS_BLOCK_UNTRUSTED = "1";

export const SHARP_CONCURRENCY = 1;

process.env.UV_THREADPOOL_SIZE ??= "4";

const { default: sharp } = await import("sharp");

sharp.block({ operation: ["VipsForeignLoad"] });
sharp.unblock({
  operation: [
    "VipsForeignLoadJpegBuffer",
    "VipsForeignLoadPngBuffer",
    "VipsForeignLoadWebpBuffer",
  ],
});

sharp.cache(false);
sharp.concurrency(SHARP_CONCURRENCY);

export { sharp };

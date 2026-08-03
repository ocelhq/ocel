// The image payloads the conformance matrix is generated over, built byte by
// byte so the checked-in fixtures move only when Next's behavior does — an
// encoder upgrade on whoever regenerates them would otherwise show up as a
// diff in every hash.
import { deflateSync } from "node:zlib";

const CRC_TABLE = (() => {
  const table = new Int32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    table[n] = c;
  }
  return table;
})();

function crc32(buffer) {
  let c = ~0;
  for (const byte of buffer) c = CRC_TABLE[(c ^ byte) & 0xff] ^ (c >>> 8);
  return ~c >>> 0;
}

function pngChunk(type, data) {
  const length = Buffer.alloc(4);
  length.writeUInt32BE(data.length);
  const body = Buffer.concat([Buffer.from(type, "ascii"), data]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(body));
  return Buffer.concat([length, body, crc]);
}

// A 16x16 truecolor PNG with a diagonal split, so a transform has something to
// resample and a re-encode is not a no-op.
export function png() {
  const size = 16;
  const raw = Buffer.alloc(size * (1 + size * 3));
  for (let y = 0; y < size; y++) {
    const row = y * (1 + size * 3);
    raw[row] = 0;
    for (let x = 0; x < size; x++) {
      const px = row + 1 + x * 3;
      const light = x + y < size;
      raw[px] = light ? 0xe0 : 0x20;
      raw[px + 1] = light ? 0x40 : 0x80;
      raw[px + 2] = light ? 0x80 : 0xc0;
    }
  }
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0);
  ihdr.writeUInt32BE(size, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 2; // truecolor
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    pngChunk("IHDR", ihdr),
    pngChunk("IDAT", deflateSync(raw, { level: 9 })),
    pngChunk("IEND", Buffer.alloc(0)),
  ]);
}

// A two-frame GIF89a. Two image descriptors is exactly what Next's is-animated
// counts, and the animated branch returns the source unmodified.
export function animatedGif() {
  const header = Buffer.from("GIF89a", "ascii");
  const screen = Buffer.from([1, 0, 1, 0, 0x80, 0, 0]);
  const palette = Buffer.from([0xff, 0x00, 0x00, 0x00, 0x00, 0xff]);
  const loop = Buffer.concat([
    Buffer.from([0x21, 0xff, 0x0b]),
    Buffer.from("NETSCAPE2.0", "ascii"),
    Buffer.from([0x03, 0x01, 0x00, 0x00, 0x00]),
  ]);
  const frame = (index) =>
    Buffer.concat([
      Buffer.from([0x21, 0xf9, 0x04, 0x00, 0x0a, 0x00, 0x00]),
      Buffer.from([0x2c, 0, 0, 0, 0, 1, 0, 1, 0, 0]),
      // LZW, 2-bit codes: CLEAR, <index>, EOI.
      Buffer.from([0x02, 0x02, 0x04 | (index << 3) | 0x40, 0x01, 0x00]),
    ]);
  return Buffer.concat([
    header,
    screen,
    palette,
    loop,
    frame(0),
    frame(1),
    Buffer.from([0x3b]),
  ]);
}

// An ICO wrapping a PNG payload — the modern form, and the one whose magic
// bytes put it on Next's BYPASS_TYPES path.
export function ico() {
  const image = png();
  const header = Buffer.alloc(6);
  header.writeUInt16LE(0, 0);
  header.writeUInt16LE(1, 2); // type: icon
  header.writeUInt16LE(1, 4); // one image
  const entry = Buffer.alloc(16);
  entry[0] = 16; // width
  entry[1] = 16; // height
  entry.writeUInt16LE(1, 4); // color planes
  entry.writeUInt16LE(32, 6); // bits per pixel
  entry.writeUInt32LE(image.length, 8);
  entry.writeUInt32LE(header.length + entry.length, 12);
  return Buffer.concat([header, entry, image]);
}

export function svg() {
  return Buffer.from(
    `<svg xmlns="http://www.w3.org/2000/svg" width="16" height="16"><rect width="16" height="16" fill="#4080c0"/></svg>\n`,
    "utf8",
  );
}

export function notAnImage() {
  return Buffer.from("this is not an image\n", "utf8");
}

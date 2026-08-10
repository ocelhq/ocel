import { describe, expect, test } from "vitest";
import {
  AVIF,
  BMP,
  GIF,
  HEIC,
  ICO,
  JPEG,
  JXL,
  PNG,
  SNIFF_WINDOW,
  SVG,
  TIFF,
  WEBP,
  detectContentType,
  isAnimated,
} from "../src/sniff.mjs";
import { animatedGif, ico, solid, stillGif, svg } from "./images.mjs";

describe("magic bytes", () => {
  test("recognises the formats a browser will be sent", async () => {
    expect(detectContentType(await solid("jpeg"))).toBe(JPEG);
    expect(detectContentType(await solid("png"))).toBe(PNG);
    expect(detectContentType(await solid("webp"))).toBe(WEBP);
    expect(detectContentType(stillGif())).toBe(GIF);
    expect(detectContentType(svg())).toBe(SVG);
    expect(detectContentType(ico())).toBe(ICO);
    expect(detectContentType(new Uint8Array([0x42, 0x4d, 0, 0]))).toBe(BMP);
    expect(detectContentType(new Uint8Array([0xff, 0x0a, 0, 0]))).toBe(JXL);
  });

  test("a third byte of 0x01 does not make a non-image an ICO", () => {
    expect(detectContentType(new Uint8Array([0x4d, 0x5a, 0x01, 0x00]))).toBe(null);
    expect(detectContentType(new Uint8Array([0x1f, 0x8b, 0x01, 0x00]))).toBe(null);
    expect(detectContentType(new Uint8Array([0x50, 0x4b, 0x01, 0x02]))).toBe(null);
  });

  test("a BMP whose third byte is 0x01 is still a BMP", () => {
    expect(detectContentType(new Uint8Array([0x42, 0x4d, 0x01, 0x00, 0x00, 0x00]))).toBe(BMP);
  });

  test("the remaining literal zero bytes are matched as literals", () => {
    expect(detectContentType(new Uint8Array([0x49, 0x49, 0x2a, 0x00]))).toBe(TIFF);
    expect(detectContentType(new Uint8Array([0x49, 0x49, 0x2a, 0x01]))).toBe(null);
    const jxl = new Uint8Array([
      0x00, 0x00, 0x00, 0x0c, 0x4a, 0x58, 0x4c, 0x20, 0x0d, 0x0a, 0x87, 0x0a,
    ]);
    expect(detectContentType(jxl)).toBe(JXL);
  });

  test("the length prefixes stay wildcards", () => {
    for (const size of [0x00, 0x18, 0xff]) {
      const avif = new Uint8Array(16);
      avif.set([size, size, size, size], 0);
      avif.set([0x66, 0x74, 0x79, 0x70, 0x61, 0x76, 0x69, 0x66], 4);
      expect(detectContentType(avif)).toBe(AVIF);
      const webp = new Uint8Array(16);
      webp.set([0x52, 0x49, 0x46, 0x46], 0);
      webp.set([size, size, size, size], 4);
      webp.set([0x57, 0x45, 0x42, 0x50], 8);
      expect(detectContentType(webp)).toBe(WEBP);
    }
  });

  test("recognises the ISO-BMFF brands past their length prefix", () => {
    const heic = new Uint8Array(16);
    heic.set([0x00, 0x00, 0x00, 0x18], 0);
    heic.set([0x66, 0x74, 0x79, 0x70, 0x68, 0x65, 0x69, 0x63], 4);
    expect(detectContentType(heic)).toBe(HEIC);
  });

  test("an XML declaration counts as SVG, as it does upstream", () => {
    expect(detectContentType(new TextEncoder().encode('<?xml version="1.0"?><svg/>'))).toBe(SVG);
  });

  test("is null for anything unrecognised", () => {
    expect(detectContentType(new Uint8Array(0))).toBe(null);
    expect(detectContentType(new TextEncoder().encode("<!DOCTYPE html><html>"))).toBe(null);
    expect(detectContentType(new TextEncoder().encode("#!/bin/sh\n"))).toBe(null);
    expect(detectContentType(new TextEncoder().encode("%PDF-1.7\n"))).toBe(null);
  });

  test("looks at the first 1024 bytes only", () => {
    const buried = new Uint8Array(SNIFF_WINDOW + 8);
    buried.set([0xff, 0xd8, 0xff], SNIFF_WINDOW);
    expect(detectContentType(buried)).toBe(null);
    const early = new Uint8Array(SNIFF_WINDOW + 8);
    early.set([0xff, 0xd8, 0xff], 0);
    expect(detectContentType(early)).toBe(JPEG);
  });
});

describe("animation", () => {
  test("a GIF with two frames is animated and one with a single frame is not", () => {
    expect(isAnimated(animatedGif(), GIF)).toBe(true);
    expect(isAnimated(stillGif(), GIF)).toBe(false);
  });

  test("a still PNG and a still WebP are not animated", async () => {
    expect(isAnimated(await solid("png"), PNG)).toBe(false);
    expect(isAnimated(await solid("webp"), WEBP)).toBe(false);
  });

  test("an acTL chunk marks an APNG", () => {
    const apng = new Uint8Array(64);
    apng.set([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a], 0);
    apng.set([0x61, 0x63, 0x54, 0x4c], 12);
    expect(isAnimated(apng, PNG)).toBe(true);
  });

  test("an ANIM chunk marks an animated WebP", () => {
    const webp = new Uint8Array(64);
    webp.set([0x52, 0x49, 0x46, 0x46], 0);
    webp.set([0x57, 0x45, 0x42, 0x50], 8);
    webp.set([0x41, 0x4e, 0x49, 0x4d], 20);
    expect(isAnimated(webp, WEBP)).toBe(true);
  });

  test("a type that cannot hold an animation is never animated", () => {
    expect(isAnimated(svg(), SVG)).toBe(false);
    expect(isAnimated(ico(), ICO)).toBe(false);
  });
});

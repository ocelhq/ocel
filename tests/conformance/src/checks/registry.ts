import { checkBlob } from "./blob";
import { checkEnv } from "./env";
import { checkHttp } from "./http";
import { checkIsr } from "./isr";
import { checkLinks } from "./links";
import { checkNative } from "./native";
import { checkPostgres } from "./postgres";
import { checkRevalidate } from "./revalidate";
import { checkStatic } from "./static";
import type { Capability, Check } from "../types";

export const checks = {
  http: checkHttp,
  static: checkStatic,
  postgres: checkPostgres,
  blob: checkBlob,
  env: checkEnv,
  native: checkNative,
  isr: checkIsr,
  revalidate: checkRevalidate,
  links: checkLinks,
} satisfies Record<Capability, Check>;

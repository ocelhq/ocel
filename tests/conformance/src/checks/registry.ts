import { checkBlob } from "./blob";
import { checkBytecode } from "./bytecode";
import { checkEnv } from "./env";
import { checkHttp } from "./http";
import { checkGolden } from "./golden";
import { checkIsr } from "./isr";
import { checkLinks } from "./links";
import { checkNative } from "./native";
import { checkPostgres } from "./postgres";
import { checkProxy } from "./proxy";
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
  proxy: checkProxy,
  golden: checkGolden,
  bytecode: checkBytecode,
  links: checkLinks,
} satisfies Record<Capability, Check>;

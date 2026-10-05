import { boot } from "./boot.mjs";
import { readPortBind } from "./host.mjs";

boot({ bind: () => readPortBind(process.env) });

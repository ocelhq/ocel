import { URL_KEY } from "./definition.js";
import { EnvValueError } from "./errors.js";
import { readDelivered } from "./value.js";

/** What this deployment knows about itself. Ocel writes it; nothing declares it. */
export interface Deployment {
  /**
   * The absolute url this app is served on, scheme and all —
   * `https://web-j-1.ocel.site` deployed, `http://localhost:3000` under
   * `ocel dev`. It is the first production hostname the app declares, falling
   * back to the project's, and the derived preview hostname on a preview.
   *
   * A client bundle reads the value inlined at build time, so adding a
   * hostname with `ocel domain add` and not deploying again leaves the browser
   * holding the hostname the last build was given. Deploy to move it.
   *
   * Throws {@link EnvValueError} where no value was delivered, which is a
   * deploy whose project declares no production domain at all.
   */
  readonly url: string;
}

/** This deployment, as the running app sees it. */
export const deployment: Deployment = {
  get url(): string {
    const url = readDelivered(URL_KEY);
    if (url === undefined) {
      throw new EnvValueError(
        `'${URL_KEY}' was not delivered to this app. Ocel writes it from the hostname the deploy serves the app on, so a project that declares no production domain has none to write: add one under \`domains.production\` and deploy again.`,
      );
    }
    return url;
  },
};

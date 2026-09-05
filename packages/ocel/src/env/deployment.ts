import { URL_KEY } from "./definition.js";
import { EnvValueError } from "./errors.js";
import { readDelivered } from "./value.js";

/** What this deployment knows about itself. Ocel writes it; nothing declares it. */
export interface Deployment {
  /**
   * The absolute url this app is served on, scheme and all —
   * `https://web-j-1.ocel.site` deployed, `http://localhost:3000` under
   * `ocel dev`. It is the first production hostname the app declares, and
   * the project's own hostnames where the app is the first one `apps`
   * names — the deploy serves those on that app alone. On a preview it is
   * the derived preview hostname.
   *
   * Present under `ocel dev` always, and on a deploy that gives this app a
   * hostname. An app past the first that declares none of its own is served
   * on no hostname, and is handed no url: reading it then throws
   * {@link EnvValueError}. Declare `domains.production` on the app.
   *
   * A client bundle reads the value inlined at build time, so adding a
   * hostname with `ocel domain add` and not deploying again leaves the browser
   * holding the hostname the last build was given. Deploy to move it.
   */
  readonly url: string;
}

/** This deployment, as the running app sees it. */
export const deployment: Deployment = {
  get url(): string {
    const url = readDelivered(URL_KEY);
    if (url === undefined) {
      throw new EnvValueError(
        `'${URL_KEY}' was not delivered to this app. Ocel writes it from the hostname the deploy serves the app on, and this app is served on none: add one under \`domains.production\` on the app, or on the project if this is the first app it names, and deploy again.`,
      );
    }
    return url;
  },
};

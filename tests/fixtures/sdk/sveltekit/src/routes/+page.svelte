<script lang="ts">
import { PUBLIC_GREETING, PUBLIC_OCEL_URL } from "$app/env/public";

let { data } = $props();
</script>

<p
  data-public-value={PUBLIC_GREETING}
  data-deployment-url={PUBLIC_OCEL_URL ?? ""}
  data-sensitive-digest={data.sensitiveDigest}
>
  rendered on the server, then read again in the browser
</p>

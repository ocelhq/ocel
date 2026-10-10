<script lang="ts">
  import { onMount } from "svelte";
  import { PUBLIC_API_URL, PUBLIC_RETRIES } from "$app/env/public";

  let { data } = $props();
  let client = $state("pending");
  onMount(() => {
    client = JSON.stringify({ PUBLIC_API_URL, PUBLIC_RETRIES, retriesType: typeof PUBLIC_RETRIES });
  });
</script>

<pre id="server">{JSON.stringify(data.server)}</pre>
<pre id="client">{client}</pre>

<script lang="ts">
let { form } = $props();
</script>

<form method="POST">
  <input name="message" />
  <button>send</button>
</form>
{#if form}<p data-echoed={form.echoed}>{form.echoed}</p>{/if}

<script lang="ts">
  import type { Model } from "../lib/types";
  import { pendingLoads, onToggleLoad } from "../stores/modelLoad";
  import { forceUnloadSingleModel, unloadSingleModel } from "../stores/api";
  import { Play, PowerOff, OctagonX, Loader2 } from "@lucide/svelte";

  interface Props {
    model: Model;
    /** "md" for list rows (size-7), "sm" for the detail header (size-5). */
    size?: "md" | "sm";
  }

  let { model, size = "md" }: Props = $props();

  let btnSize = $derived(size === "sm" ? "size-5 rounded-sm" : "size-7 rounded-md");
  let iconSize = $derived(size === "sm" ? "size-3.5" : "size-4");
  let running = $derived(model.state !== "stopped");

  function stopGracefully(): void {
    void unloadSingleModel(model.id).catch((e) => console.error(e));
  }

  function forceStop(): void {
    void forceUnloadSingleModel(model.id).catch((e) => console.error(e));
  }
</script>

{#if running}
  <button
    type="button"
    class="text-muted-foreground hover:bg-accent hover:text-accent-foreground flex {btnSize} shrink-0 items-center justify-center"
    title="Stop gracefully (cancels a start, then kills after the timeout)"
    aria-label="Stop model gracefully"
    onclick={stopGracefully}
  >
    <PowerOff class={iconSize} />
  </button>
  <button
    type="button"
    class="text-muted-foreground hover:bg-destructive/15 hover:text-destructive flex {btnSize} shrink-0 items-center justify-center"
    title="Force stop now (SIGKILL)"
    aria-label="Force stop model"
    onclick={forceStop}
  >
    <OctagonX class={iconSize} />
  </button>
{:else}
  <button
    type="button"
    class="text-muted-foreground hover:bg-accent hover:text-accent-foreground flex {btnSize} shrink-0 items-center justify-center disabled:opacity-50"
    title={$pendingLoads[model.id] ? "Cancel" : "Load"}
    aria-label="Load model"
    onclick={() => onToggleLoad(model)}
  >
    {#if $pendingLoads[model.id]}
      <Loader2 class="{iconSize} animate-spin" />
    {:else}
      <Play class={iconSize} />
    {/if}
  </button>
{/if}

<script lang="ts">
  import { onMount } from "svelte";
  import { params } from "svelte-spa-router";
  import { fetchSelectorConfigs, selectorConfigs } from "../stores/api";
  import CopyableId from "../components/CopyableId.svelte";
  import Tag from "../components/Tag.svelte";
  import * as Card from "$lib/components/ui/card/index.js";
  import SelectorDetailsTab from "../components/selector/SelectorDetailsTab.svelte";

  let selectorId = $derived($params?.id ?? "");
  let selector = $derived($selectorConfigs.find((s) => s.id === selectorId));
  let hasName = $derived(!!selector?.name && selector.name !== selector.id);

  let loading = $state(true);
  let loadError = $state("");

  onMount(async () => {
    loading = true;
    loadError = "";
    try {
      await fetchSelectorConfigs();
    } catch (error) {
      loadError = error instanceof Error ? error.message : String(error);
    } finally {
      loading = false;
    }
  });
</script>

<div class="flex h-full flex-col gap-4 overflow-y-auto p-2">
  {#if loading && !selector}
    <Card.Root class="shrink-0 p-6">
      <p class="text-muted-foreground text-sm">Loading selector…</p>
    </Card.Root>
  {:else if loadError && !selector}
    <Card.Root class="shrink-0 p-6">
      <p class="text-destructive text-sm">{loadError}</p>
      <a href="/selectors" class="text-primary text-sm hover:underline">Back to Selectors</a>
    </Card.Root>
  {:else if !selector}
    <Card.Root class="shrink-0 p-6">
      <p class="text-muted-foreground">Selector “{selectorId}” not found.</p>
      <a href="/selectors" class="text-primary hover:underline">Back to Selectors</a>
    </Card.Root>
  {:else}
    <Card.Root class="shrink-0 gap-0 overflow-hidden py-0">
      <Card.Header class="shrink-0 gap-2 px-4 py-3">
        <div class="flex items-start gap-2">
          <div class="flex min-w-0 flex-1 flex-col gap-1">
            {#if hasName}
              <Card.Title class="text-lg break-words">{selector.name}</Card.Title>
            {:else}
              <Card.Title class="-ml-1 text-lg">
                <CopyableId value={selector.id} />
              </Card.Title>
            {/if}
            <div class="text-muted-foreground flex flex-wrap items-center gap-x-2 gap-y-1 text-sm">
              {#if hasName}
                <CopyableId value={selector.id} class="-ml-1 font-mono text-xs" />
              {/if}
              <Tag class="px-1.5 text-[0.625rem] uppercase">{selector.strategy}</Tag>
              {#if selector.unlisted}
                <Tag class="px-1.5 text-[0.625rem] uppercase">unlisted</Tag>
              {/if}
              <span class="text-xs">
                {selector.targets?.length ?? 0}
                {selector.targets?.length === 1 ? "target" : "targets"}
              </span>
            </div>
          </div>
        </div>
        {#if selector.description}
          <p class="text-muted-foreground text-sm"><em>{selector.description}</em></p>
        {/if}
      </Card.Header>
    </Card.Root>

    <SelectorDetailsTab {selector} />
  {/if}
</div>

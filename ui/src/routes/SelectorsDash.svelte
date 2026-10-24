<script lang="ts">
  import { onMount } from "svelte";
  import { link } from "svelte-spa-router";
  import {
    fetchSelectorConfigs,
    models,
    selectorConfigs,
  } from "../stores/api";
  import type { Model, ModelStatus, SelectorConfig } from "../lib/types";
  import * as Card from "$lib/components/ui/card/index.js";
  import Tag from "../components/Tag.svelte";
  import { ChevronRight, Split } from "@lucide/svelte";

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

  function targetModel(target: string): Model | undefined {
    return (
      $models.find((model) => model.id === target) ??
      $models.find((model) => model.aliases?.includes(target))
    );
  }

  // Mirrors how /v1/models decides whether a selector is loaded: pin only
  // looks at its first target, the other strategies at every target.
  function selectorState(selector: SelectorConfig): ModelStatus {
    const targets =
      selector.strategy === "pin" ? (selector.targets ?? []).slice(0, 1) : selector.targets ?? [];
    let state: ModelStatus = "stopped";
    for (const target of targets) {
      const model = targetModel(target);
      if (!model) continue;
      if (model.state === "ready") return "ready";
      if (model.state === "starting" || model.state === "stopping") state = model.state;
    }
    return state;
  }

  function stateDotClass(state: ModelStatus): string {
    if (state === "ready") return "bg-success";
    if (state === "starting" || state === "stopping") return "bg-warning";
    return "bg-muted-foreground/40";
  }
</script>

<div class="flex h-full flex-col gap-4 overflow-y-auto p-2">
  <Card.Root class="shrink-0 gap-0 overflow-hidden py-0">
    <Card.Header class="shrink-0 gap-2 border-b px-4 py-3">
      <div class="flex items-center gap-2">
        <Split class="size-5" />
        <Card.Title class="text-lg">Selectors</Card.Title>
        <span class="text-muted-foreground text-sm">({$selectorConfigs.length})</span>
        <span class="text-muted-foreground ml-auto text-xs uppercase tracking-wide">
          {$selectorConfigs.filter((selector) => selector.unlisted).length} unlisted
        </span>
      </div>
    </Card.Header>
  </Card.Root>

  {#if loadError}
    <Card.Root class="shrink-0 p-6">
      <p class="text-destructive text-sm">{loadError}</p>
    </Card.Root>
  {:else if loading && $selectorConfigs.length === 0}
    <Card.Root class="shrink-0 p-6">
      <p class="text-muted-foreground text-sm">Loading selectors…</p>
    </Card.Root>
  {:else if $selectorConfigs.length === 0}
    <Card.Root class="shrink-0 p-6">
      <p class="text-muted-foreground text-sm">No selectors configured.</p>
    </Card.Root>
  {:else}
    <Card.Root class="shrink-0 gap-0 overflow-hidden py-0">
      <Card.Content class="p-0">
        <div class="divide-y">
          {#each $selectorConfigs as selector (selector.id)}
            {@const state = selectorState(selector)}
            <div class="hover:bg-muted/50 flex items-start gap-3 px-4 py-2.5">
              <span
                class={`mt-1.5 size-2.5 shrink-0 rounded-full ${stateDotClass(state)}`}
                role="img"
                aria-label={`Selector ${state}`}
              ></span>
              <div class="min-w-0 flex-1">
                <a
                  href="/selectors/{encodeURIComponent(selector.id)}"
                  use:link
                  class="block min-w-0 hover:underline"
                >
                  <div class="truncate text-sm font-medium">
                    {selector.name || selector.id}
                  </div>
                  {#if selector.name && selector.name !== selector.id}
                    <div class="text-muted-foreground truncate font-mono text-xs">{selector.id}</div>
                  {/if}
                  {#if selector.description}
                    <div class="text-muted-foreground truncate text-xs">{selector.description}</div>
                  {/if}
                </a>
                <div class="text-muted-foreground flex flex-wrap items-center gap-x-1 text-xs">
                  <span>targets:</span>
                  {#each selector.targets ?? [] as target, i (target)}
                    {#if i > 0}<span>,</span>{/if}
                    <a
                      href="/models/{encodeURIComponent(target)}"
                      use:link
                      class="hover:text-foreground hover:underline"
                    >{target}</a>
                  {/each}
                </div>
              </div>
              <span class="text-muted-foreground text-xs uppercase tracking-wide">{state}</span>
              {#if selector.strategy === "spillover" && selector.settings.spillover}
                <Tag>spillover {selector.settings.spillover}</Tag>
              {/if}
              <Tag class="px-1.5 text-[0.625rem] uppercase">{selector.strategy}</Tag>
              {#if selector.unlisted}
                <Tag class="px-1.5 text-[0.625rem] uppercase">unlisted</Tag>
              {/if}
              <a
                href="/selectors/{encodeURIComponent(selector.id)}"
                use:link
                class="text-muted-foreground hover:text-foreground shrink-0"
                aria-label={`Open selector ${selector.id}`}
              >
                <ChevronRight class="size-4" />
              </a>
            </div>
          {/each}
        </div>
      </Card.Content>
    </Card.Root>
  {/if}
</div>

<script lang="ts">
  import type { Model, ModelLaunch } from "../../lib/types";
  import { capabilityLabels } from "../../lib/capabilities";
  import { apiUrl } from "../../lib/apiPath";
  import * as Card from "$lib/components/ui/card/index.js";
  import Tag from "../Tag.svelte";

  interface Props {
    model: Model;
  }

  let { model }: Props = $props();

  let capabilities = $derived.by(() => {
    const caps = model?.capabilities ?? {};
    return Object.entries(caps).filter(([, v]) => v);
  });

  // Launch options live in the config, not in the model status payload the
  // page already has, so they are fetched per model. Peer models and selectors
  // have no local config and answer 404; their section is simply hidden.
  let launch = $state<ModelLaunch | null>(null);
  let launchLoading = $state(false);
  let launchError = $state("");

  $effect(() => {
    const id = model?.id;
    if (!id) return;
    const controller = new AbortController();
    launch = null;
    launchError = "";
    launchLoading = true;
    fetch(apiUrl(`/api/models/launch/${encodeURIComponent(id)}`), {
      signal: controller.signal,
    })
      .then(async (response) => {
        if (response.status === 404) return null;
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        return (await response.json()) as ModelLaunch;
      })
      .then((data) => {
        launch = data;
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        launchError = error instanceof Error ? error.message : String(error);
      })
      .finally(() => {
        if (!controller.signal.aborted) launchLoading = false;
      });
    return () => controller.abort();
  });

  function ttlLabel(ttl: number | undefined): string {
    if (ttl === undefined) return "";
    return ttl === 0 ? "never" : `${ttl}s`;
  }
</script>

<div class="flex flex-col gap-3">
  {#if launchLoading && !launch && !launchError}
    <Card.Root class="shrink-0 gap-0 overflow-hidden py-0">
      <Card.Content class="text-muted-foreground p-3 text-sm">
        Loading launch options…
      </Card.Content>
    </Card.Root>
  {:else if launchError}
    <Card.Root class="shrink-0 gap-0 overflow-hidden py-0">
      <Card.Header class="border-b px-4 py-2">
        <Card.Title class="text-sm font-semibold">Launch options</Card.Title>
      </Card.Header>
      <Card.Content class="p-3">
        <span class="text-muted-foreground text-sm">Unavailable ({launchError}).</span>
      </Card.Content>
    </Card.Root>
  {:else if launch}
    <Card.Root class="shrink-0 gap-0 overflow-hidden py-0">
      <Card.Header class="border-b px-4 py-2">
        <Card.Title class="text-sm font-semibold">Launch options</Card.Title>
      </Card.Header>
      <Card.Content class="p-3">
        <dl class="grid grid-cols-[minmax(7rem,auto)_1fr] items-start gap-x-4 gap-y-3 text-sm">
          <dt class="text-muted-foreground pt-0.5">Command</dt>
          <dd class="min-w-0">
            {#if launch.cmd}
              <pre class="bg-muted/50 overflow-x-auto rounded p-2 font-mono text-xs whitespace-pre-wrap break-all">{launch.cmd}</pre>
            {:else}
              <span class="text-muted-foreground">none</span>
            {/if}
          </dd>

          {#if launch.cmdStop}
            <dt class="text-muted-foreground pt-0.5">Stop command</dt>
            <dd class="min-w-0">
              <pre class="bg-muted/50 overflow-x-auto rounded p-2 font-mono text-xs whitespace-pre-wrap break-all">{launch.cmdStop}</pre>
            </dd>
          {/if}

          {#if launch.env && launch.env.length > 0}
            <dt class="text-muted-foreground pt-0.5">Environment</dt>
            <dd class="min-w-0">
              <pre class="bg-muted/50 overflow-x-auto rounded p-2 font-mono text-xs whitespace-pre-wrap break-all">{launch.env.join("\n")}</pre>
            </dd>
          {/if}

          {#if launch.proxy}
            <dt class="text-muted-foreground pt-0.5">Proxy</dt>
            <dd class="font-mono text-xs break-all">{launch.proxy}</dd>
          {/if}

          {#if launch.checkEndpoint}
            <dt class="text-muted-foreground pt-0.5">Health check</dt>
            <dd class="font-mono text-xs break-all">{launch.checkEndpoint}</dd>
          {/if}

          {#if launch.ttl !== undefined}
            <dt class="text-muted-foreground pt-0.5">TTL</dt>
            <dd class="tabular-nums">{ttlLabel(launch.ttl)}</dd>
          {/if}

          {#if launch.unloadTimeout}
            <dt class="text-muted-foreground pt-0.5">Unload timeout</dt>
            <dd class="tabular-nums">{launch.unloadTimeout}s</dd>
          {/if}

          {#if launch.concurrencyLimit}
            <dt class="text-muted-foreground pt-0.5">Concurrency limit</dt>
            <dd class="tabular-nums">{launch.concurrencyLimit}</dd>
          {/if}

          {#if launch.unlisted}
            <dt class="text-muted-foreground pt-0.5">Listed</dt>
            <dd>no (unlisted)</dd>
          {/if}
        </dl>
      </Card.Content>
    </Card.Root>
  {/if}

  <Card.Root class="shrink-0 gap-0 overflow-hidden py-0">
    <Card.Header class="border-b px-4 py-2">
      <Card.Title class="text-sm font-semibold">Capabilities</Card.Title>
    </Card.Header>
    <Card.Content class="p-3">
      {#if capabilities.length === 0}
        <span class="text-muted-foreground text-sm">No capabilities reported.</span>
      {:else}
        <div class="flex flex-wrap gap-1.5">
          {#each capabilities as [key] (key)}
            <Tag>{capabilityLabels[key] ?? key}</Tag>
          {/each}
        </div>
      {/if}
    </Card.Content>
  </Card.Root>
</div>

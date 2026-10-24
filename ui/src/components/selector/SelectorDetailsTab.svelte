<script lang="ts">
  import { link } from "svelte-spa-router";
  import type { SelectorConfig, SelectorRoute } from "../../lib/types";
  import * as Card from "$lib/components/ui/card/index.js";
  import Tag from "../Tag.svelte";

  interface Props {
    selector: SelectorConfig;
  }

  let { selector }: Props = $props();

  let targets = $derived(selector.targets ?? []);
  let routes = $derived(
    Object.entries(selector.routes ?? {}).sort(([a], [b]) => a.localeCompare(b)),
  );

  interface SettingRow {
    label: string;
    value: string;
    title?: string;
    mono?: boolean;
  }

  // The server applies selector defaults while loading, so these are the
  // effective values. Only show the settings the chosen strategy reads.
  let settingRows = $derived.by((): SettingRow[] => {
    const settings = selector.settings;
    const rows: SettingRow[] = [];

    if (selector.strategy === "spillover") {
      rows.push({ label: "Spillover", value: String(settings.spillover) });
    }

    if (selector.strategy === "decision") {
      rows.push(
        { label: "Decider", value: settings.decider || "none" },
        { label: "Default route", value: settings.default || "none" },
        { label: "Minimum confidence", value: String(settings.minConfidence) },
        { label: "Decision timeout", value: formatNanoseconds(settings.timeout) },
        { label: "Session mode", value: settings.mode ?? "request" },
      );
      if (settings.instructions) {
        rows.push({
          label: "Instructions",
          value: settings.instructions,
          mono: true,
        });
      }
      if (settings.mode === "sticky" || settings.mode === "adaptive") {
        rows.push(
          { label: "Session header", value: settings.sessionHeader || "X-Session-ID" },
          { label: "Session TTL", value: formatNanoseconds(settings.sessionTTL) },
          { label: "Refresh header", value: settings.refreshHeader || "X-Route-Refresh" },
          { label: "Unload decider", value: settings.unloadDecider ? "yes" : "no" },
        );
      }
      if (settings.mode === "adaptive") {
        rows.push(
          { label: "Switch margin", value: String(settings.switchMargin) },
          { label: "Switch confidence", value: String(settings.switchConfidence) },
          { label: "Minimum dwell", value: formatNanoseconds(settings.minDwell) },
        );
      }
    }

    if (settings.context) {
      rows.push({
        label: "Context override",
        value: `${settings.context.toLocaleString()} tokens`,
      });
    }

    return rows;
  });

  function formatNanoseconds(value: number | undefined): string {
    if (value === undefined || value <= 0) return "0s";
    const seconds = value / 1e9;
    if (seconds < 1) return `${Math.round(value / 1e6)}ms`;
    if (seconds % 3600 === 0) return `${seconds / 3600}h`;
    if (seconds % 60 === 0) return `${seconds / 60}m`;
    return `${Number(seconds.toFixed(3))}s`;
  }

  function metadataValue(value: unknown): string {
    return typeof value === "string" ? value : JSON.stringify(value);
  }
</script>

<div class="flex flex-col gap-3">
  <Card.Root class="shrink-0 gap-0 overflow-hidden py-0">
    <Card.Header class="border-b px-4 py-2">
      <Card.Title class="text-sm font-semibold">Configuration</Card.Title>
    </Card.Header>
    <Card.Content class="p-3">
      <dl class="grid grid-cols-[minmax(7rem,auto)_1fr] items-start gap-x-4 gap-y-3 text-sm">
        <dt class="text-muted-foreground pt-0.5">Strategy</dt>
        <dd><Tag class="px-1.5 text-[0.625rem] uppercase">{selector.strategy}</Tag></dd>

        <dt class="text-muted-foreground pt-0.5">Targets</dt>
        <dd class="flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-1">
          {#if targets.length === 0}
            <span class="text-muted-foreground">none</span>
          {:else}
            {#each targets as target (target)}
              <a
                href="/models/{encodeURIComponent(target)}"
                use:link
                class="font-mono text-xs hover:underline"
              >{target}</a>
            {/each}
          {/if}
        </dd>

        {#if settingRows.length > 0}
          {#each settingRows as row (row.label)}
            <dt class="text-muted-foreground pt-0.5">{row.label}</dt>
            <dd class:font-mono={row.mono} class="min-w-0 break-words text-sm">
              {#if row.mono}
                <pre class="bg-muted/50 overflow-x-auto rounded p-2 text-xs whitespace-pre-wrap break-all">{row.value}</pre>
              {:else}
                {row.value}
              {/if}
            </dd>
          {/each}
        {/if}

        <dt class="text-muted-foreground pt-0.5">Listed</dt>
        <dd>{selector.unlisted ? "no (unlisted)" : "yes"}</dd>

        {#if selector.metadata && Object.keys(selector.metadata).length > 0}
          <dt class="text-muted-foreground pt-0.5">Metadata</dt>
          <dd class="min-w-0">
            <dl class="grid grid-cols-[minmax(5rem,auto)_1fr] gap-x-3 gap-y-1 text-xs">
              {#each Object.entries(selector.metadata) as [key, value] (key)}
                <dt class="text-muted-foreground font-mono break-all">{key}</dt>
                <dd class="font-mono break-all">{metadataValue(value)}</dd>
              {/each}
            </dl>
          </dd>
        {/if}
      </dl>
    </Card.Content>
  </Card.Root>

  {#if selector.strategy === "decision"}
    <Card.Root class="shrink-0 gap-0 overflow-hidden py-0">
      <Card.Header class="border-b px-4 py-2">
        <Card.Title class="text-sm font-semibold">Routes</Card.Title>
      </Card.Header>
      <Card.Content class="p-0">
        {#if routes.length === 0}
          <div class="text-muted-foreground px-4 py-6 text-center text-sm">
            No routes configured.
          </div>
        {:else}
          <div class="divide-y">
            {#each routes as [label, route] (label)}
              {@const detail = route as SelectorRoute}
              <div class="flex items-start gap-3 px-4 py-2.5">
                <Tag class="shrink-0 px-1.5 text-[0.625rem] uppercase">{label}</Tag>
                <div class="min-w-0 flex-1">
                  {#if detail.description}
                    <div class="text-muted-foreground truncate text-xs">{detail.description}</div>
                  {/if}
                  <a
                    href="/models/{encodeURIComponent(detail.model)}"
                    use:link
                    class="font-mono text-xs hover:underline"
                  >{detail.model}</a>
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </Card.Content>
    </Card.Root>
  {/if}
</div>

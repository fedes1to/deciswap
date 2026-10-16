<script lang="ts">
  import { onMount } from "svelte";
  import { Button } from "$lib/components/ui/button/index.js";
  import * as Card from "$lib/components/ui/card/index.js";
  import { Input } from "$lib/components/ui/input/index.js";
  import { Label } from "$lib/components/ui/label/index.js";
  import * as Dialog from "$lib/components/ui/dialog/index.js";
  import { Check, Copy } from "@lucide/svelte";
  import { copyText } from "../../lib/clipboard";
  import { createAdminKey, listAdminKeys, revokeAdminKey, type AdminKey, type CreatedAdminKey } from "../../lib/adminApi";
  import { adminErrorMessage } from "../../stores/admin";
  import { fetchPlaygroundModels, playgroundModels } from "../../stores/api";

  const ALL_MODELS = "*";

  let keys = $state<AdminKey[]>([]);
  let loadError = $state("");
  let name = $state("");
  let selected = $state<string[]>([]);
  let createError = $state("");
  let createdKey = $state<CreatedAdminKey | null>(null);
  let keyCopied = $state(false);
  let revokeTarget = $state<AdminKey | null>(null);
  let revokeError = $state("");
  let busy = $state(false);

  // Model choices: every selector and alias /v1/models lists, sorted.
  let choices = $derived.by(() => {
    const ids = new Set<string>();
    for (const model of $playgroundModels) {
      ids.add(model.id);
      for (const alias of model.aliases ?? []) ids.add(alias);
    }
    return [...ids].sort((a, b) => a.localeCompare(b, undefined, { numeric: true }));
  });

  async function load(): Promise<void> {
    try {
      keys = await listAdminKeys();
      loadError = "";
    } catch (cause) {
      loadError = adminErrorMessage(cause);
    }
  }

  onMount(() => {
    void load();
    void fetchPlaygroundModels();
  });

  function toggleModel(id: string, checked: boolean): void {
    if (id === ALL_MODELS) {
      selected = checked ? [ALL_MODELS] : [];
      return;
    }
    const without = selected.filter((m) => m !== ALL_MODELS && m !== id);
    selected = checked ? [...without, id] : without;
  }

  async function create(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    createError = "";
    if (selected.length === 0) {
      createError = "Choose at least one model, or * for all models.";
      return;
    }
    busy = true;
    try {
      createdKey = await createAdminKey(name.trim(), selected);
      keyCopied = false;
      name = "";
      selected = [];
      await load();
    } catch (cause) {
      createError = adminErrorMessage(cause);
    } finally {
      busy = false;
    }
  }

  async function copyKey(): Promise<void> {
    if (!createdKey) return;
    keyCopied = await copyText(createdKey.key);
  }

  async function revoke(): Promise<void> {
    if (!revokeTarget) return;
    busy = true;
    revokeError = "";
    try {
      await revokeAdminKey(revokeTarget.id);
      revokeTarget = null;
      await load();
    } catch (cause) {
      revokeError = adminErrorMessage(cause);
    } finally {
      busy = false;
    }
  }

  function formatTime(value: string | null): string {
    return value ? new Date(value).toLocaleString() : "never";
  }
</script>

<div class="space-y-4">
  <Card.Root class="p-4">
    <Card.Header class="px-0">
      <Card.Title class="text-base font-semibold">API keys</Card.Title>
      <Card.Description>
        Revoking a key stops it working immediately; no reload is needed. Keys are sent as
        <code>Authorization: Bearer</code> to the inference API.
      </Card.Description>
    </Card.Header>
    {#if loadError}
      <p class="text-destructive text-sm">{loadError}</p>
    {:else if keys.length === 0}
      <p class="text-muted-foreground text-sm">No API keys yet.</p>
    {:else}
      <div class="overflow-x-auto">
        <table class="w-full text-left text-sm">
          <thead class="text-muted-foreground text-xs">
            <tr>
              <th class="py-1 pr-3 font-medium">Name</th>
              <th class="py-1 pr-3 font-medium">Prefix</th>
              <th class="py-1 pr-3 font-medium">Models</th>
              <th class="py-1 pr-3 font-medium">Created</th>
              <th class="py-1 pr-3 font-medium">Last used</th>
              <th class="py-1 pr-3 font-medium">Status</th>
              <th class="py-1 font-medium"><span class="sr-only">Actions</span></th>
            </tr>
          </thead>
          <tbody>
            {#each keys as key (key.id)}
              <tr class="border-border border-t">
                <td class="py-2 pr-3">{key.name}</td>
                <td class="py-2 pr-3 font-mono text-xs">{key.prefix}</td>
                <td class="py-2 pr-3 text-xs">{key.models.join(", ")}</td>
                <td class="py-2 pr-3 text-xs">{formatTime(key.createdAt)}</td>
                <td class="py-2 pr-3 text-xs">{formatTime(key.lastUsedAt)}</td>
                <td class="py-2 pr-3">
                  <span class={key.active ? "text-success" : "text-muted-foreground"}>
                    {key.active ? "active" : "revoked"}
                  </span>
                </td>
                <td class="py-2 text-right">
                  {#if key.active}
                    <Button variant="outline" size="sm" onclick={() => (revokeTarget = key)}>Revoke</Button>
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {/if}
  </Card.Root>

  {#if createdKey}
    <Card.Root class="border-warning p-4">
      <Card.Header class="px-0">
        <Card.Title class="text-base font-semibold">New key: {createdKey.name}</Card.Title>
        <Card.Description>
          This is the only time the full key is shown. Copy it now and store it somewhere safe.
        </Card.Description>
      </Card.Header>
      <div class="flex items-center gap-2">
        <code class="bg-muted min-w-0 flex-1 rounded px-2 py-1 font-mono text-sm break-all select-all">{createdKey.key}</code>
        <Button variant="outline" size="sm" onclick={copyKey}>
          {#if keyCopied}<Check class="text-success" />{:else}<Copy />{/if}
          {keyCopied ? "Copied" : "Copy"}
        </Button>
      </div>
      <Button variant="ghost" size="sm" class="mt-2 self-start" onclick={() => (createdKey = null)}>
        I have saved this key
      </Button>
    </Card.Root>
  {/if}

  <Card.Root class="p-4">
    <Card.Header class="px-0">
      <Card.Title class="text-base font-semibold">Create key</Card.Title>
    </Card.Header>
    <form class="space-y-3" onsubmit={create}>
      <div class="space-y-1">
        <Label for="key-name">Name</Label>
        <Input id="key-name" bind:value={name} required />
      </div>
      <fieldset class="space-y-1">
        <legend class="mb-1 text-sm font-medium">Models</legend>
        <p class="text-muted-foreground text-xs">
          Choose specific models or aliases, or <code>*</code> for every model. <code>*</code> cannot be combined with others.
        </p>
        <div class="grid max-h-60 grid-cols-1 gap-1 overflow-auto sm:grid-cols-2">
          <label class="flex items-center gap-2 text-sm font-medium">
            <input
              type="checkbox"
              checked={selected.includes(ALL_MODELS)}
              onchange={(e) => toggleModel(ALL_MODELS, e.currentTarget.checked)}
            />
            * (all models)
          </label>
          {#each choices as id (id)}
            <label class="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={selected.includes(id)}
                disabled={selected.includes(ALL_MODELS)}
                onchange={(e) => toggleModel(id, e.currentTarget.checked)}
              />
              <span class="font-mono text-xs">{id}</span>
            </label>
          {/each}
        </div>
      </fieldset>
      {#if createError}
        <p class="text-destructive text-sm">{createError}</p>
      {/if}
      <Button type="submit" disabled={busy}>Create key</Button>
    </form>
  </Card.Root>
</div>

<Dialog.Root open={revokeTarget !== null} onOpenChange={(open) => { if (!open) revokeTarget = null; }}>
  <Dialog.Content>
    <Dialog.Header>
      <Dialog.Title>Revoke "{revokeTarget?.name}"?</Dialog.Title>
      <Dialog.Description>
        Requests using this key will be refused immediately. This cannot be undone.
      </Dialog.Description>
    </Dialog.Header>
    {#if revokeError}
      <p class="text-destructive text-sm">{revokeError}</p>
    {/if}
    <Dialog.Footer>
      <Button variant="outline" onclick={() => (revokeTarget = null)}>Cancel</Button>
      <Button variant="destructive" disabled={busy} onclick={revoke}>Revoke key</Button>
    </Dialog.Footer>
  </Dialog.Content>
</Dialog.Root>

<script lang="ts">
  import { onMount } from "svelte";
  import { Button } from "$lib/components/ui/button/index.js";
  import * as Card from "$lib/components/ui/card/index.js";
  import ConfigEditor from "./ConfigEditor.svelte";
  import { Label } from "$lib/components/ui/label/index.js";
  import { AdminApiError, listConfigFiles, saveConfig, validateConfig, type ConfigFile } from "../../lib/adminApi";
  import { adminErrorMessage } from "../../stores/admin";

  let files = $state<ConfigFile[]>([]);
  let selectedPath = $state("");
  let content = $state("");
  let hash = $state("");
  let loadError = $state("");
  let message = $state("");
  let messageIsError = $state(false);
  let conflict = $state(false);
  let busy = $state(false);

  let selectedFile = $derived(files.find((f) => f.path === selectedPath) ?? null);
  let dirty = $derived(selectedFile !== null && content !== selectedFile.content);

  function setMessage(text: string, isError: boolean): void {
    message = text;
    messageIsError = isError;
  }

  async function load(): Promise<void> {
    try {
      files = await listConfigFiles();
      loadError = "";
      if (!selectedPath || !files.some((f) => f.path === selectedPath)) {
        selectFile(files[0]?.path ?? "");
      }
    } catch (cause) {
      loadError = adminErrorMessage(cause);
    }
  }

  function selectFile(path: string): void {
    selectedPath = path;
    const file = files.find((f) => f.path === path);
    content = file?.content ?? "";
    hash = file?.hash ?? "";
    conflict = false;
    setMessage("", false);
  }

  async function reloadFromDisk(): Promise<void> {
    await load();
    selectFile(selectedPath);
  }

  onMount(() => {
    void load();
  });

  async function validate(): Promise<void> {
    busy = true;
    try {
      const result = await validateConfig(selectedPath, content);
      setMessage(
        result.valid
          ? `Valid: ${result.models} model(s), ${result.peers} peer(s).`
          : "Configuration is not valid.",
        !result.valid,
      );
    } catch (cause) {
      setMessage(adminErrorMessage(cause), true);
    } finally {
      busy = false;
    }
  }

  async function save(): Promise<void> {
    if (!confirm("Saving reloads deciswap and restarts model processes. Continue?")) return;
    busy = true;
    try {
      const result = await saveConfig(selectedPath, content, hash);
      hash = result.hash;
      // Reload the listing so the editor's baseline matches what is now on disk.
      await load();
      selectFile(selectedPath);
      setMessage(result.reloading ? "Saved. deciswap is reloading." : "Saved.", false);
      conflict = false;
    } catch (cause) {
      if (cause instanceof AdminApiError && cause.status === 409) {
        conflict = true;
        setMessage("The file changed on disk since it was loaded. Reload it to continue editing.", true);
      } else {
        setMessage(adminErrorMessage(cause), true);
      }
    } finally {
      busy = false;
    }
  }
</script>

<div class="space-y-4">
  <Card.Root class="p-4">
    <Card.Header class="px-0">
      <Card.Title class="text-base font-semibold">Configuration</Card.Title>
      <Card.Description>
        Saving reloads deciswap and restarts model processes. Running requests may be interrupted.
      </Card.Description>
    </Card.Header>

    {#if loadError}
      <p class="text-destructive text-sm">{loadError}</p>
    {:else if files.length === 0}
      <p class="text-muted-foreground text-sm">No configuration files found.</p>
    {:else}
      <div class="space-y-3">
        <div class="space-y-1">
          <Label for="config-file">File</Label>
          <select
            id="config-file"
            class="border-input bg-background h-9 w-full rounded-md border px-2 font-mono text-sm"
            value={selectedPath}
            onchange={(e) => selectFile(e.currentTarget.value)}
          >
            {#each files as file (file.path)}
              <option value={file.path}>{file.path.split("/").pop()} — {file.path} ({file.kind})</option>
            {/each}
          </select>
        </div>

        <ConfigEditor bind:value={content} />

        {#if message}
          <p class={messageIsError ? "text-destructive text-sm" : "text-success text-sm"}>{message}</p>
        {/if}

        <div class="flex flex-wrap items-center gap-2">
          <span class="text-muted-foreground text-xs tabular-nums">
            {content.split("\n").length} lines · {content.length} bytes
          </span>
          <Button variant="outline" onclick={validate} disabled={busy || !selectedPath}>Validate</Button>
          <Button onclick={save} disabled={busy || !selectedPath || conflict}>Save &amp; Reload</Button>
          {#if conflict || dirty}
            <Button variant="ghost" onclick={reloadFromDisk} disabled={busy}>Reload from disk</Button>
          {/if}
          {#if dirty}
            <span class="text-muted-foreground text-xs">Unsaved changes</span>
          {/if}
        </div>
      </div>
    {/if}
  </Card.Root>
</div>

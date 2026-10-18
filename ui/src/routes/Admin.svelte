<script lang="ts">
  import { onMount } from "svelte";
  import { Tabs, TabsContent, TabsList, TabsTrigger } from "$lib/components/ui/tabs/index.js";
  import SetupForm from "../components/admin/SetupForm.svelte";
  import LoginForm from "../components/admin/LoginForm.svelte";
  import SecurityTab from "../components/admin/SecurityTab.svelte";
  import ApiKeysTab from "../components/admin/ApiKeysTab.svelte";
  import ConfigTab from "../components/admin/ConfigTab.svelte";
  import { adminSession, refreshAdminSession } from "../stores/admin";

  let loading = $state(true);
  let tab = $state("security");

  onMount(async () => {
    await refreshAdminSession();
    loading = false;
  });
</script>

<div class="mx-auto space-y-4" class:max-w-3xl={tab !== "config"} class:max-w-none={tab === "config"}>
  {#if loading && $adminSession === null}
    <p class="text-muted-foreground text-sm">Loading admin session…</p>
  {:else if $adminSession?.setupRequired}
    <SetupForm />
  {:else if !$adminSession?.authenticated}
    <LoginForm />
  {:else}
    <div class="flex items-center justify-between gap-2">
      <p class="text-muted-foreground text-sm">Signed in as <span class="font-medium text-foreground">{$adminSession.username}</span></p>
    </div>
    <Tabs bind:value={tab}>
      <TabsList variant="line">
        <TabsTrigger value="security">Security</TabsTrigger>
        <TabsTrigger value="keys">API Keys</TabsTrigger>
        <TabsTrigger value="config">Config</TabsTrigger>
      </TabsList>
      <TabsContent value="security" class="mt-4">
        <SecurityTab />
      </TabsContent>
      <TabsContent value="keys" class="mt-4">
        <ApiKeysTab />
      </TabsContent>
      <TabsContent value="config" class="mt-4">
        <ConfigTab />
      </TabsContent>
    </Tabs>
  {/if}
</div>

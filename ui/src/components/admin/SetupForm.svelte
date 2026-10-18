<script lang="ts">
  import { Button } from "$lib/components/ui/button/index.js";
  import * as Card from "$lib/components/ui/card/index.js";
  import { Input } from "$lib/components/ui/input/index.js";
  import { Label } from "$lib/components/ui/label/index.js";
  import { AdminApiError, MIN_PASSWORD_LENGTH, passwordTooShort, setupAdmin } from "../../lib/adminApi";
  import { adminErrorMessage, refreshAdminSession } from "../../stores/admin";

  let token = $state("");
  let username = $state("");
  let password = $state("");
  let error = $state("");
  let submitting = $state(false);

  async function submit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    error = "";
    if (passwordTooShort(password)) {
      error = `Password must be at least ${MIN_PASSWORD_LENGTH} characters.`;
      return;
    }
    submitting = true;
    try {
      await setupAdmin(token.trim(), username.trim(), password);
      password = "";
      token = "";
      await refreshAdminSession();
    } catch (cause) {
      if (cause instanceof AdminApiError && cause.status === 401) error = "Setup token is incorrect.";
      else error = adminErrorMessage(cause);
    } finally {
      submitting = false;
    }
  }
</script>

<Card.Root class="p-4">
  <Card.Header class="px-0">
    <Card.Title class="text-lg font-bold">Set up admin account</Card.Title>
    <Card.Description>
      Enter the setup token from the root-only file named in the startup log (llama-swap-admin-setup-token). It is accepted once.
    </Card.Description>
  </Card.Header>
  <form class="space-y-3" onsubmit={submit}>
    <div class="space-y-1">
      <Label for="setup-token">Setup token</Label>
      <Input id="setup-token" type="password" autocomplete="off" bind:value={token} required />
    </div>
    <div class="space-y-1">
      <Label for="setup-username">Username</Label>
      <Input id="setup-username" autocomplete="username" bind:value={username} required />
    </div>
    <div class="space-y-1">
      <Label for="setup-password">Password</Label>
      <Input
        id="setup-password"
        type="password"
        autocomplete="new-password"
        bind:value={password}
        required
      />
      <p class="text-muted-foreground text-xs">At least {MIN_PASSWORD_LENGTH} characters.</p>
    </div>
    {#if error}
      <p class="text-destructive text-sm">{error}</p>
    {/if}
    <Button type="submit" disabled={submitting}>Create admin account</Button>
  </form>
</Card.Root>

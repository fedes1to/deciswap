<script lang="ts">
  import { Button } from "$lib/components/ui/button/index.js";
  import * as Card from "$lib/components/ui/card/index.js";
  import { Input } from "$lib/components/ui/input/index.js";
  import { Label } from "$lib/components/ui/label/index.js";
  import { AdminApiError, loginAdmin } from "../../lib/adminApi";
  import { adminErrorMessage, refreshAdminSession } from "../../stores/admin";

  let username = $state("");
  let password = $state("");
  let code = $state("");
  let needsCode = $state(false);
  let error = $state("");
  let submitting = $state(false);

  async function submit(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    error = "";
    submitting = true;
    try {
      await loginAdmin(username.trim(), password, code.trim());
      password = "";
      code = "";
      needsCode = false;
      await refreshAdminSession();
    } catch (cause) {
      if (cause instanceof AdminApiError && cause.totpRequired) {
        needsCode = true;
        error = "Enter the code from your authenticator app or a recovery code.";
      } else if (cause instanceof AdminApiError && cause.status === 401) {
        error = "Invalid username or password.";
      } else {
        error = adminErrorMessage(cause);
      }
    } finally {
      submitting = false;
    }
  }
</script>

<Card.Root class="p-4">
  <Card.Header class="px-0">
    <Card.Title class="text-lg font-bold">Admin sign in</Card.Title>
  </Card.Header>
  <form class="space-y-3" onsubmit={submit}>
    <div class="space-y-1">
      <Label for="login-username">Username</Label>
      <Input id="login-username" autocomplete="username" bind:value={username} required />
    </div>
    <div class="space-y-1">
      <Label for="login-password">Password</Label>
      <Input
        id="login-password"
        type="password"
        autocomplete="current-password"
        bind:value={password}
        required
      />
    </div>
    {#if needsCode}
      <div class="space-y-1">
        <Label for="login-code">Two-factor code</Label>
        <Input
          id="login-code"
          autocomplete="one-time-code"
          inputmode="text"
          bind:value={code}
          required
        />
      </div>
    {/if}
    {#if error}
      <p class="text-destructive text-sm">{error}</p>
    {/if}
    <Button type="submit" disabled={submitting}>Sign in</Button>
  </form>
</Card.Root>

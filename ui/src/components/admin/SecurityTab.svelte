<script lang="ts">
  import { Button } from "$lib/components/ui/button/index.js";
  import * as Card from "$lib/components/ui/card/index.js";
  import { Input } from "$lib/components/ui/input/index.js";
  import { Label } from "$lib/components/ui/label/index.js";
  import { copyText } from "../../lib/clipboard";
  import {
    beginTwoFactor,
    changeAdminPassword,
    confirmTwoFactor,
    disableTwoFactor,
    logoutAdmin,
    MIN_PASSWORD_LENGTH,
    passwordTooShort,
    type TwoFactorEnrollment,
  } from "../../lib/adminApi";
  import { adminErrorMessage, adminSession, refreshAdminSession } from "../../stores/admin";

  let currentPassword = $state("");
  let newPassword = $state("");
  let passwordMessage = $state("");
  let passwordError = $state("");

  let enrollment = $state<TwoFactorEnrollment | null>(null);
  let enrollCode = $state("");
  let recoveryCodes = $state<string[]>([]);
  let twoFactorError = $state("");
  let secretCopied = $state(false);

  let disablePassword = $state("");
  let disableCode = $state("");
  let disableError = $state("");

  let busy = $state(false);

  async function run(action: () => Promise<void>, onError: (message: string) => void): Promise<void> {
    busy = true;
    try {
      await action();
    } catch (cause) {
      onError(adminErrorMessage(cause));
    } finally {
      busy = false;
    }
  }

  async function changePassword(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    passwordError = "";
    passwordMessage = "";
    if (passwordTooShort(newPassword)) {
      passwordError = `New password must be at least ${MIN_PASSWORD_LENGTH} characters.`;
      return;
    }
    await run(async () => {
      await changeAdminPassword(currentPassword, newPassword);
      currentPassword = "";
      newPassword = "";
      passwordMessage = "Password changed. Other sessions were signed out.";
    }, (message) => (passwordError = message));
  }

  async function startEnrollment(): Promise<void> {
    twoFactorError = "";
    await run(async () => {
      enrollment = await beginTwoFactor();
      enrollCode = "";
    }, (message) => (twoFactorError = message));
  }

  async function confirmEnrollment(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    twoFactorError = "";
    await run(async () => {
      recoveryCodes = await confirmTwoFactor(enrollCode.trim());
      enrollment = null;
      enrollCode = "";
      await refreshAdminSession();
    }, (message) => (twoFactorError = message));
  }

  async function copySecret(): Promise<void> {
    if (!enrollment) return;
    secretCopied = await copyText(enrollment.secret);
  }

  async function disableEnrollment(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    disableError = "";
    await run(async () => {
      await disableTwoFactor(disablePassword, disableCode.trim());
      disablePassword = "";
      disableCode = "";
      await refreshAdminSession();
    }, (message) => (disableError = message));
  }

  async function logout(): Promise<void> {
    await run(async () => {
      await logoutAdmin();
      await refreshAdminSession();
    }, (message) => (passwordError = message));
  }
</script>

<div class="space-y-4">
  <Card.Root class="p-4">
    <Card.Header class="px-0">
      <Card.Title class="text-base font-semibold">Change password</Card.Title>
      <Card.Description>Changing the password signs out every other session.</Card.Description>
    </Card.Header>
    <form class="space-y-3" onsubmit={changePassword}>
      <div class="space-y-1">
        <Label for="current-password">Current password</Label>
        <Input
          id="current-password"
          type="password"
          autocomplete="current-password"
          bind:value={currentPassword}
          required
        />
      </div>
      <div class="space-y-1">
        <Label for="new-password">New password</Label>
        <Input
          id="new-password"
          type="password"
          autocomplete="new-password"
          bind:value={newPassword}
          required
        />
        <p class="text-muted-foreground text-xs">At least {MIN_PASSWORD_LENGTH} characters.</p>
      </div>
      {#if passwordError}
        <p class="text-destructive text-sm">{passwordError}</p>
      {/if}
      {#if passwordMessage}
        <p class="text-success text-sm">{passwordMessage}</p>
      {/if}
      <Button type="submit" disabled={busy}>Change password</Button>
    </form>
  </Card.Root>

  <Card.Root class="p-4">
    <Card.Header class="px-0">
      <Card.Title class="text-base font-semibold">Two-factor authentication</Card.Title>
      <Card.Description>
        Status:
        <span class="font-medium">{$adminSession?.twoFactorEnabled ? "enabled" : "disabled"}</span>
      </Card.Description>
    </Card.Header>

    {#if recoveryCodes.length > 0}
      <div class="border-warning space-y-2 rounded-md border p-3">
        <p class="text-sm font-semibold">Save these recovery codes now.</p>
        <p class="text-muted-foreground text-xs">
          They are shown only once. Each code signs you in a single time if you lose your authenticator.
        </p>
        <ul class="grid grid-cols-2 gap-1 font-mono text-sm select-all">
          {#each recoveryCodes as code (code)}
            <li>{code}</li>
          {/each}
        </ul>
        <Button variant="outline" size="sm" onclick={() => (recoveryCodes = [])}>
          I have saved these codes
        </Button>
      </div>
    {/if}

    {#if $adminSession?.twoFactorEnabled}
      <form class="mt-3 space-y-3" onsubmit={disableEnrollment}>
        <p class="text-muted-foreground text-sm">
          Disabling two-factor authentication needs your password and a current code or recovery code.
        </p>
        <div class="space-y-1">
          <Label for="disable-password">Password</Label>
          <Input id="disable-password" type="password" autocomplete="current-password" bind:value={disablePassword} required />
        </div>
        <div class="space-y-1">
          <Label for="disable-code">Authenticator or recovery code</Label>
          <Input id="disable-code" autocomplete="one-time-code" bind:value={disableCode} required />
        </div>
        {#if disableError}
          <p class="text-destructive text-sm">{disableError}</p>
        {/if}
        <Button type="submit" variant="destructive" disabled={busy}>Disable two-factor</Button>
      </form>
    {:else if enrollment}
      <div class="mt-3 space-y-3">
        <p class="text-sm">
          Add this account to your authenticator app using the setup key or the URI below, then enter the
          six-digit code it shows.
        </p>
        <div class="space-y-1">
          <Label>Setup key</Label>
          <div class="flex items-center gap-2">
            <code class="bg-muted rounded px-2 py-1 font-mono text-sm select-all break-all">{enrollment.secret}</code>
            <Button variant="outline" size="sm" onclick={copySecret}>
              {secretCopied ? "Copied" : "Copy"}
            </Button>
          </div>
        </div>
        <div class="space-y-1">
          <Label>otpauth URI</Label>
          <code class="bg-muted block rounded px-2 py-1 font-mono text-xs break-all select-all">{enrollment.otpauthUri}</code>
        </div>
        <form class="space-y-3" onsubmit={confirmEnrollment}>
          <div class="space-y-1">
            <Label for="enroll-code">Code from your app</Label>
            <Input id="enroll-code" inputmode="numeric" autocomplete="one-time-code" bind:value={enrollCode} required />
          </div>
          {#if twoFactorError}
            <p class="text-destructive text-sm">{twoFactorError}</p>
          {/if}
          <div class="flex gap-2">
            <Button type="submit" disabled={busy}>Confirm and enable</Button>
            <Button variant="outline" onclick={() => (enrollment = null)}>Cancel</Button>
          </div>
        </form>
      </div>
    {:else}
      <div class="mt-3 space-y-2">
        {#if twoFactorError}
          <p class="text-destructive text-sm">{twoFactorError}</p>
        {/if}
        <Button onclick={startEnrollment} disabled={busy}>Set up two-factor</Button>
      </div>
    {/if}
  </Card.Root>

  <Card.Root class="p-4">
    <Card.Header class="px-0">
      <Card.Title class="text-base font-semibold">Session</Card.Title>
    </Card.Header>
    <Button variant="outline" onclick={logout} disabled={busy}>Sign out</Button>
  </Card.Root>
</div>

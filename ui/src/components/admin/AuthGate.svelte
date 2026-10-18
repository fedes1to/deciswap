<script lang="ts">
  import SetupForm from "./SetupForm.svelte";
  import LoginForm from "./LoginForm.svelte";
  import { adminSession } from "../../stores/admin";
</script>

<!-- The only thing an unauthenticated browser gets: first-run setup or the
     admin login form. Rendered outside the app shell so no route, sidebar or
     header is reachable until the session exists. -->
<div class="flex min-h-screen w-full items-center justify-center p-4">
  <div class="w-full max-w-sm">
    {#if $adminSession?.setupRequired}
      <SetupForm />
    {:else}
      <LoginForm />
    {/if}
  </div>
</div>

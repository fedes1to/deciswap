import { get, writable } from "svelte/store";
import {
  AdminApiError,
  getAdminSession,
  needsAdminLogin,
  requestPathname,
  type AdminSession,
} from "../lib/adminApi";

/** Admin session as last reported by the server; null until the first check completes. */
export const adminSession = writable<AdminSession | null>(null);

export async function refreshAdminSession(): Promise<void> {
  try {
    adminSession.set(await getAdminSession());
  } catch (error) {
    console.error("Failed to load admin session:", error);
  }
}

/** Message for a failed admin call. A 401 re-checks the session, so an expired session falls back to the login page. */
export function adminErrorMessage(error: unknown): string {
  if (error instanceof AdminApiError && error.status === 401) void refreshAdminSession();
  return error instanceof Error ? error.message : String(error);
}

/** Sends the browser to #/admin when a non-admin API call is refused with 401 on a server that has an admin account. */
export function installAdminLoginRedirect(): () => void {
  const originalFetch = window.fetch;
  window.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const response = await originalFetch(input, init);
    const session = get(adminSession);
    const adminConfigured = session !== null && !session.setupRequired;
    if (needsAdminLogin(requestPathname(input), response.status, adminConfigured)) {
      window.location.hash = "#/admin";
    }
    return response;
  }) as typeof fetch;
  return () => {
    window.fetch = originalFetch;
  };
}

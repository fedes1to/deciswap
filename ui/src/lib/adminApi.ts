import { apiUrl } from "./apiPath";
import { csrfHeaders } from "./csrf";

export const MIN_PASSWORD_LENGTH = 12;

/** The server measures passwords in bytes, so the hint counts bytes too. */
export function passwordTooShort(password: string): boolean {
  return new TextEncoder().encode(password).length < MIN_PASSWORD_LENGTH;
}

export interface AdminSession {
  setupRequired: boolean;
  authenticated: boolean;
  username: string;
  twoFactorEnabled: boolean;
}

export interface AdminAccount {
  username: string;
  twoFactorEnabled: boolean;
}

export interface TwoFactorEnrollment {
  secret: string;
  otpauthUri: string;
}

export interface AdminKey {
  id: string;
  name: string;
  prefix: string;
  models: string[];
  createdAt: string;
  lastUsedAt: string | null;
  revokedAt: string | null;
  active: boolean;
}

export interface CreatedAdminKey extends AdminKey {
  key: string;
}

export interface ConfigFile {
  path: string;
  kind: "config" | "dir";
  hash: string;
  content: string;
}

export interface ConfigValidation {
  valid: boolean;
  models: number;
  peers: number;
}

export interface ConfigSaveResult {
  hash: string;
  reloading: boolean;
}

/** Failed /api/admin call. `totpRequired` is set when a login needs a 2FA code. */
export class AdminApiError extends Error {
  readonly status: number;
  readonly totpRequired: boolean;

  constructor(status: number, message: string, totpRequired = false) {
    super(message);
    this.name = "AdminApiError";
    this.status = status;
    this.totpRequired = totpRequired;
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const response = await fetch(apiUrl(path), {
    method,
    headers: body === undefined ? { ...csrfHeaders } : { ...csrfHeaders, "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!response.ok) {
    let message = `HTTP ${response.status}`;
    let totpRequired = false;
    try {
      const data = (await response.json()) as { error?: string; totpRequired?: boolean };
      if (data.error) message = data.error;
      totpRequired = data.totpRequired === true;
    } catch {
      // Non-JSON error body: keep the status-based message.
    }
    throw new AdminApiError(response.status, message, totpRequired);
  }
  if (response.status === 204) return undefined as T;
  return (await response.json()) as T;
}

export function getAdminSession(): Promise<AdminSession> {
  return request<AdminSession>("GET", "/api/admin/session");
}

export function setupAdmin(token: string, username: string, password: string): Promise<AdminAccount> {
  return request<AdminAccount>("POST", "/api/admin/setup", { token, username, password });
}

export function loginAdmin(username: string, password: string, code: string): Promise<AdminAccount> {
  return request<AdminAccount>("POST", "/api/admin/login", { username, password, code });
}

export function logoutAdmin(): Promise<void> {
  return request<void>("POST", "/api/admin/logout");
}

export function changeAdminPassword(currentPassword: string, newPassword: string): Promise<void> {
  return request<void>("POST", "/api/admin/password", { currentPassword, newPassword });
}

export function beginTwoFactor(): Promise<TwoFactorEnrollment> {
  return request<TwoFactorEnrollment>("POST", "/api/admin/2fa/begin");
}

export async function confirmTwoFactor(code: string): Promise<string[]> {
  const data = await request<{ recoveryCodes: string[] }>("POST", "/api/admin/2fa/confirm", { code });
  return data.recoveryCodes;
}

export function disableTwoFactor(password: string, code: string): Promise<void> {
  return request<void>("POST", "/api/admin/2fa/disable", { password, code });
}

export async function listAdminKeys(): Promise<AdminKey[]> {
  const data = await request<{ keys: AdminKey[] }>("GET", "/api/admin/keys");
  return data.keys;
}

export function createAdminKey(name: string, models: string[]): Promise<CreatedAdminKey> {
  return request<CreatedAdminKey>("POST", "/api/admin/keys", { name, models });
}

export function revokeAdminKey(id: string): Promise<void> {
  return request<void>("DELETE", `/api/admin/keys/${encodeURIComponent(id)}`);
}

export async function listConfigFiles(): Promise<ConfigFile[]> {
  const data = await request<{ files: ConfigFile[] }>("GET", "/api/admin/config");
  return data.files;
}

export function validateConfig(path: string, content: string): Promise<ConfigValidation> {
  return request<ConfigValidation>("POST", "/api/admin/config/validate", { path, content });
}

export function saveConfig(path: string, content: string, expectedHash: string): Promise<ConfigSaveResult> {
  return request<ConfigSaveResult>("PUT", "/api/admin/config", { path, content, expectedHash });
}

/** Whether a failed request means the admin must sign in: a 401 from a non-admin /api/ route on a server with an admin account. */
export function needsAdminLogin(pathname: string, status: number, adminConfigured: boolean): boolean {
  if (status !== 401 || !adminConfigured) return false;
  const at = pathname.indexOf("/api/");
  return at >= 0 && !pathname.startsWith("/api/admin/", at);
}

/** Pathname of a fetch target, resolved against the page so relative inputs work. */
export function requestPathname(input: RequestInfo | URL): string {
  const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
  return new URL(url, window.location.href).pathname;
}

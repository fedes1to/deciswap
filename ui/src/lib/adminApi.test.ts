import { describe, expect, it } from "vitest";
import { needsAdminLogin, passwordTooShort } from "./adminApi";

describe("needsAdminLogin", () => {
  it("routes a 401 from a non-admin API route to login when an admin exists", () => {
    expect(needsAdminLogin("/api/profiles", 401, true)).toBe(true);
    expect(needsAdminLogin("/asubpath/llama-swap/api/metrics/activity", 401, true)).toBe(true);
  });

  it("leaves admin routes alone so their own errors are shown in place", () => {
    expect(needsAdminLogin("/api/admin/password", 401, true)).toBe(false);
    expect(needsAdminLogin("/asubpath/api/admin/keys", 401, true)).toBe(false);
  });

  it("ignores other statuses, non-API paths, and servers without an admin", () => {
    expect(needsAdminLogin("/api/profiles", 403, true)).toBe(false);
    expect(needsAdminLogin("/v1/models", 401, true)).toBe(false);
    expect(needsAdminLogin("/api/profiles", 401, false)).toBe(false);
  });
});

describe("passwordTooShort", () => {
  it("counts bytes, not characters", () => {
    expect(passwordTooShort("abcdefghijk")).toBe(true);
    expect(passwordTooShort("abcdefghijkl")).toBe(false);
    // 5 x "é" is 10 UTF-8 bytes (too short); 6 x "é" is exactly 12 bytes.
    expect(passwordTooShort("ééééé")).toBe(true);
    expect(passwordTooShort("éééééé")).toBe(false);
  });
});

/**
 * Header every same-origin request that changes state must carry. The server
 * rejects cookie-authenticated unsafe requests that lack it, so browser-side
 * POST/PUT/PATCH/DELETE calls spread this into their headers.
 */
export const csrfHeaders = { "X-Requested-With": "llama-swap" } as const;

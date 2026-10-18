---
title: API keys, admin auth, and keeping secrets out of your config
summary: Legacy apiKeys grant full access; scoped keys and an admin account with TOTP limit what a client can reach.
category: guides
tags: [security, api-keys, auth, admin, totp, recovery-codes, sessions, csrf, secrets, env, access-control, permissions]
config_keys: [apiKeys, macros, peers.*.apiKey, models.*.env, store.path]
updated: 2026-10-16
---

# API keys, admin auth, and keeping secrets out of your config

llama-swap has two ways to authenticate a client, and they are independent:

- **`apiKeys`** — the original list of static keys. Every key in it has full
  access to everything the proxy served before, and nothing else. Still
  supported, still all-or-nothing.
- **An admin account with scoped API keys** — sign in once, then create keys
  limited to specific models, optionally protected by TOTP. Admin state lives
  in its own database, not in your config file.

Secrets stay out of the config either way; see
[Keep the keys out of the file](#keep-the-keys-out-of-the-file).

## Requiring a key

By default llama-swap is unauthenticated. Add `apiKeys` and every request needs
one:

```yaml
apiKeys:
  - "sk-hunter2"
```

Clients may present it as `Authorization: Bearer <key>`, `x-api-key: <key>`, or
HTTP Basic. The web UI and everything under `/api/` are covered too.

A handful of static files stay reachable without a key regardless:
`/favicon.ico`, `/ui/site.webmanifest`, and the two icons it references
(`/ui/web-app-manifest-192x192.png`, `/ui/web-app-manifest-512x512.png`).
Browsers fetch all of these themselves — the favicon for the tab, the
manifest and its icons to decide whether to offer installing llama-swap as a
PWA — and have no way to attach your API key to those requests.

Generate a real one:

```console
$ printf "sk-%s\n" "$(head -c 48 /dev/urandom | base64)"
```

Multiple keys are allowed, which is how you rotate without downtime: add the
new key, move clients over, remove the old one.

All keys in the list are equivalent: no per-key model limits, expiry,
last-used timestamp, or rate limit. The one thing they cannot reach is the
admin API — `/api/admin/*` always needs an admin session.

**That is the risk of staying on `apiKeys`.** Each entry is a credential for
the whole proxy: it reaches the UI, the ops endpoints and every model, it
carries no name to tell clients apart, and revoking it means editing the config
and reloading (or waiting for `-watch-config` to notice). Create a scoped key
per client instead and drop the legacy list once clients have moved over —
removing the last `apiKeys` entry does not reopen the proxy, because a scoped
key still counts as configured.

**`apiKeys` is not a substitute for a firewall.** llama-swap starts processes
on your machine. Do not expose it to the internet on the strength of a bearer
token alone.

## The admin account

The admin account is what creates scoped keys, enrols two-factor
authentication, and edits the config from the UI. It has no representation in
`config.yaml` at all.

### Set up the first admin

On the first start with no admin account, llama-swap generates a **setup
token**, prints it once to the proxy log as a clearly marked line, and keeps it
in memory only. Open the UI:

```
http://localhost:8080/ui/#/admin
```

With no admin account the Admin page renders the setup form; afterwards it
renders the login form, and once you are signed in, the Security, API Keys and
Config tabs. The sidebar shows an Admin entry only while signed in.

Scripted setup:

```console
$ curl -i -X POST http://localhost:8080/api/admin/setup \
    -H 'Content-Type: application/json' \
    -d '{"token":"<token from the log>","username":"admin","password":"correct-horse-battery"}'
HTTP/1.1 201 Created
Set-Cookie: llama_swap_session=...
{"username":"admin","twoFactorEnabled":false}
```

- The token is valid **until it is used or llama-swap restarts**. It is never
  written to disk, and a restart before setup prints a new one — a token copied
  from an earlier run is 401.
- 409 means an admin already exists. 400 means the username or password was
  rejected: the username is trimmed, at most 64 bytes, and may not contain
  control characters; the password must be 12–1024 bytes.
- Repeated failures are throttled per IP: 429 with a `Retry-After` header. The
  same throttle guards login, password change and 2FA confirmation.

Creating an admin account also locks the proxy down — anonymous callers are
refused from then on. See [Who can reach what](#who-can-reach-what).

### Signing in

```console
$ curl -i -c /tmp/swap.cookies -X POST http://localhost:8080/api/admin/login \
    -H 'Content-Type: application/json' \
    -d '{"username":"admin","password":"correct-horse-battery"}'
HTTP/1.1 200 OK
Set-Cookie: llama_swap_session=...
{"username":"admin","twoFactorEnabled":false}
```

The cookie is the session: `-c` stores it, `-b` sends it. A session lives 12
hours after its last request and at most 7 days after it was created. With
two-factor enabled, add the code:

```console
$ curl -i -c /tmp/swap.cookies -X POST http://localhost:8080/api/admin/login \
    -H 'Content-Type: application/json' \
    -d '{"username":"admin","password":"correct-horse-battery","code":"123456"}'
```

A correct password with no code gets 401 with `"totpRequired":true` in the body,
so a client knows to prompt for the code instead of reporting bad credentials.

Cookie-authenticated requests that change state (POST, PUT, PATCH, DELETE) must
also carry the CSRF header, on every route:

```console
$ curl -i -b /tmp/swap.cookies -X POST http://localhost:8080/api/admin/logout \
    -H 'X-Requested-With: llama-swap'
HTTP/1.1 204 No Content
```

Without it the request is refused with 403 `{"error":"missing CSRF header"}`.
The web UI sends it on every unsafe fetch; curl and scripts have to add it
themselves. Requests authenticated by an API key do not need it — the header is
what stops a browser page from riding your session cookie.

Other session endpoints:

- `GET /api/admin/session` — `{"setupRequired","authenticated","username","twoFactorEnabled"}`,
  needs no session. This is what decides between the setup and login forms.
- `POST /api/admin/password` — `{"currentPassword","newPassword"}` → 204, and
  every *other* session is revoked.
- `POST /api/admin/logout` — 204, clears the cookie.

### Two-factor authentication

Optional, and off until you enrol. `begin` returns a secret and an
`otpauth://` URI for an authenticator app:

```console
$ curl -s -b /tmp/swap.cookies -X POST http://localhost:8080/api/admin/2fa/begin
{"secret":"JBSWY3DPEHPK3PXP","otpauthUri":"otpauth://totp/llama-swap:admin?secret=JBSWY3DPEHPK3PXP&issuer=llama-swap&algorithm=SHA1&digits=6&period=30"}
```

Confirm with a code the app produces for that secret:

```console
$ curl -s -b /tmp/swap.cookies -X POST http://localhost:8080/api/admin/2fa/confirm \
    -H 'Content-Type: application/json' \
    -H 'X-Requested-With: llama-swap' \
    -d '{"code":"123456"}'
{"recoveryCodes":["4K7QP-2M9XR","..."]}
```

- 2FA is enabled only when `confirm` succeeds. `begin` on an account that
  already has it enabled, or `confirm` without a preceding `begin`, is 409.
- The eight recovery codes are shown **once** and each is good for **one**
  sign-in. They are stored hashed, so llama-swap cannot show them again.
- Anywhere a code is asked for, a TOTP code or a recovery code will do. Codes
  are RFC 6238, SHA1, 6 digits, 30-second steps; a step that was already used
  is refused, so the same code cannot be replayed.
- The TOTP secret is stored in plaintext in the auth database, because it has
  to be read to verify codes. Anyone who can read that file can mint valid
  codes for your account — treat it as a credential.

Disabling 2FA needs the password **and** a code, so a stolen session alone is
not enough:

```console
$ curl -i -b /tmp/swap.cookies -X POST http://localhost:8080/api/admin/2fa/disable \
    -H 'Content-Type: application/json' \
    -H 'X-Requested-With: llama-swap' \
    -d '{"password":"correct-horse-battery","code":"4K7QP-2M9XR"}'
HTTP/1.1 204 No Content
```

That clears the secret and deletes every recovery code.

### Lost the authenticator

Use a recovery code: sign in with it in place of the TOTP code. Each works
once, so count how many you have left.

If the codes are gone too, reset the admin account from the command line:

```console
$ llama-swap -reset-admin -config /path/to/config.yaml
```

`-reset-admin` loads the config, deletes the admin account, the recovery codes
and every session, prints a one-line confirmation and exits 0. **API keys are
kept.** It has to point at the same auth database the proxy uses, so pass the
same `-config` (or `-config-dir`) you run llama-swap with. Start llama-swap
again and run setup with the new setup token from `llama-swap-admin-setup-token` next to the auth database (mode 0600; the path is printed in the startup log).

## Scoped API keys

Created in the Admin page's **API Keys** tab, or over the API. A key is bound
to a list of models and can be revoked the moment you stop trusting it.

```console
$ curl -i -b /tmp/swap.cookies -X POST http://localhost:8080/api/admin/keys \
    -H 'Content-Type: application/json' \
    -H 'X-Requested-With: llama-swap' \
    -d '{"name":"laptop","models":["qwen3-coder-30b"]}'
HTTP/1.1 201 Created
{"key":"sk-ls-...","id":"...","name":"laptop","prefix":"sk-ls-Ab3dE",
 "models":["qwen3-coder-30b"],"createdAt":"...","lastUsedAt":null,
 "revokedAt":null,"active":true}
```

- `models` takes model IDs, aliases, selector IDs, or `*` **alone** for every
  model. `*` mixed with other entries, an empty list, an unknown name, or a
  key name that is blank or longer than 64 characters is 400 — both the name
  and the models are validated against the config the instance is running.
- `key` is the plaintext, returned **once** and never again. The database keeps
  only a hash; the list shows `prefix`, the first 12 characters, for telling
  keys apart. Copy it somewhere safe before closing the response.
- Revoke with `DELETE /api/admin/keys/{id}` → 204. It takes effect
  immediately, with no reload: the next request with that key is 403. An
  unknown or already revoked id is 404, so a revocation is never silently a
  no-op.
- `GET /api/admin/keys` lists everything with `createdAt`, `lastUsedAt`,
  `revokedAt` and `active`. Revoked keys stay in the list; `lastUsedAt` is
  refreshed at most once a minute per key.

Revoking, with the `id` from the list:

```console
$ curl -i -b /tmp/swap.cookies -X DELETE http://localhost:8080/api/admin/keys/<id> \
    -H 'X-Requested-With: llama-swap'
HTTP/1.1 204 No Content
```

Using one:

```console
$ curl -i http://localhost:8080/v1/chat/completions \
    -H 'Authorization: Bearer sk-ls-...' \
    -H 'Content-Type: application/json' \
    -d '{"model":"qwen3-coder-30b","messages":[{"role":"user","content":"hi"}]}'
HTTP/1.1 200 OK

$ curl -i http://localhost:8080/v1/chat/completions \
    -H 'Authorization: Bearer sk-ls-...' \
    -H 'Content-Type: application/json' \
    -d '{"model":"gpt-oss-120b","messages":[{"role":"user","content":"hi"}]}'
HTTP/1.1 403 Forbidden
{"error":"..."}
```

### What a scoped key can reach

Only the model routes, plus `/v1/models` (and `/models`) filtered to the models
the key is allowed. Everything else — the UI, `/metrics`, `/logs`, `/api/mcp`,
the rest of `/api/*` — answers 403. Scoped keys cannot manage keys, 2FA or the
config: that needs an admin session.

A model is allowed when `*` is in the key's list, when the requested name is in
the list, or when the model's base model is in the list (so a key for a
`setParamsByID` alias covers the model it points at). The name that is checked
is the one the client asked for, **before** profiles, selectors and rewrites
run. A request naming no model, or one that cannot be resolved, is 403.

### Selectors and profiles

Both rewrite the model after the check, so the allowed list is about the names
clients send, not the models that end up running:

- **Selectors.** Allowing a selector name (say `auto`) permits its routed
  targets through the internal sub-requests, so one entry covers the whole
  group. The reverse does not hold: a key listing only `auto`'s targets still
  gets 403 when a client asks for `auto`.
- **Profiles.** A key allowed `m1` gets whatever the active profile pins `m1`
  to — the pin decides the real model, and the key's access follows it. A key
  listing only the pin's *target* gets 403 for `m1`, because that is not the
  name it was allowed.

So a key limited to a stable name can reach different hardware depending on
which profile is active, and a key limited to `auto` can reach models that are
not in its list. List the names your clients actually send, and see
`guides/routing/profiles-and-selectors` for how the rewriting works.

## Who can reach what

Authentication is enforced as soon as **any** of these is true: `apiKeys` is
non-empty, at least one scoped key exists (revoked ones count), or an admin
account exists. With none of them, only the admin API is protected — the
default for a fresh instance.

| route class | routes | anonymous | legacy `apiKeys` | scoped key | admin session |
| --- | --- | --- | --- | --- | --- |
| public | `/health`, `/wol-health`, `/`, `/favicon.ico`, the UI manifest and its icons, `GET /api/admin/session`, `POST /api/admin/setup`, `POST /api/admin/login` | allow | allow | allow | allow |
| UI | `GET /ui/*` | allow while nothing is configured, and again once an admin account exists so the login form can load; otherwise 401 | allow | 403 | allow |
| admin API | `/api/admin/*` (everything not in the public row) | 401 | 403 | 403 | allow |
| ops | `/api/*` (non-admin), `/logs`, `/logs/stream*`, `/metrics`, `/unload`, `/running`, `/api/mcp`, `/comfyui*`, `/audiocpp*` | allow while nothing is configured; otherwise 401 | allow | 403 | allow |
| model requests | the model-dispatched routes (`/v1/chat/completions`, `/v1/responses`, `/v1/completions`, `/v1/embeddings`, `/v1/images/*`, `/v1/audio/*`, `/rerank`, `/sdapi/*`, `/infill`, …) and `/upstream/*` | allow while nothing is configured; otherwise 401 | allow | allowed models only; otherwise 403 | allow |
| listings | `GET /v1/models`, `GET /models` | allow while nothing is configured; otherwise 401 | allow, full list | allow, filtered to allowed models | allow, full list |

- 401 bodies are `{"error":"..."}`. `WWW-Authenticate: Basic realm="llama-swap"`
  is added on the ops and model classes only, never on the admin API or the UI.
  Once an admin account exists it is dropped there too: the only credential
  the admin accepts is the session cookie, which a Basic challenge cannot
  produce, so the rejection reads `unauthorized: admin login required` and the
  web UI shows the login form instead of a browser credential dialog. The
  challenge remains while a deployment relies on legacy `apiKeys` alone.
- The web UI is gated as a whole: with an admin account configured, an
  unauthenticated browser gets the login form (or the first-run setup form)
  and nothing else. The SPA bundle itself is still served so the form can
  load, but every route, model and ops call it makes answers 401 until the
  session exists.
- 403 bodies are `{"error":"..."}`.
- An unsafe method (POST, PUT, PATCH, DELETE) authenticated by the session
  cookie needs `X-Requested-With: llama-swap` on any of these classes.
- `/upstream/<model>/...` reads the model out of the path, so the same
  allowed-model rule applies there.
- `GET /api/models/launch/<model>` is stricter than the ops class: it returns
  the model's command line and `env`, which can contain credentials, so it
  requires the admin session even when a legacy `apiKeys` key would pass the
  ops gate. The response is served with `Cache-Control: no-store`.

## Where the admin state lives

The admin account, recovery codes, sessions and scoped keys live in a SQLite
auth database, opened separately from the activity store:

- `store.path` is set → the same file as your activity logs, opened as a second
  handle in WAL mode.
- Otherwise → `llama-swap-auth.db` next to your `-config` file, or inside
  `-config-dir` when that is the only source given.
- If that file cannot be opened, llama-swap logs a warning and keeps admin
  state in memory: setup, login and keys all work, but the admin account, keys
  and sessions are gone on restart, and a new setup token is written each time to `llama-swap-admin-setup-token` (mode 0600) beside the auth database
  (the in-memory store starts out with no admin).

The store is opened once at startup and reused across config reloads, so
editing the config does not log you out. No config key controls any of this:
deleting the auth database is a full reset of admin state, while
`-reset-admin` drops the admin account, the recovery codes and the sessions
and keeps the API keys.

## Editing the config from the admin API

The Admin page's **Config** tab is these three endpoints:

- `GET /api/admin/config` → `{"files":[{"path","kind","hash","content"}]}`, one
  entry per config source (`kind` is `config` for `-config`, `dir` for a file
  merged from `-config-dir`), read fresh from disk with the sha256 of its
  content.
- `POST /api/admin/config/validate` — `{"path","content"}` →
  `{"valid":true,"models":N,"peers":M}`, or 400 with the loader's message. It
  validates the merged config with your new content in memory; nothing is
  written.
- `PUT /api/admin/config` — `{"path","content","expectedHash"}` →
  `{"hash":"<new>","reloading":true}`. `path` must be one of the loaded
  sources. If the file's current hash is not `expectedHash` the write is
  refused with 409 `{"error":"file changed on disk; reload and merge"}`, so an
  editor saving at the same time cannot be silently overwritten.

A successful `PUT` writes atomically (temp file, fsync, rename), copies the
original to `<file>.bak` — one backup, overwritten on the next save — and then
reloads. Reload is the same path `-watch-config` uses: llama-swap rebuilds its
server and **stops the model processes that are running**, so the next request
cold-starts them. Saving a config is not a no-op; do it when a brief stop is
acceptable.

## When it goes wrong

| what you see | why | what to do |
| --- | --- | --- |
| every `/api/*` request suddenly 401s after you set up the admin account | creating an admin account turns enforcement on; anonymous callers lose access | give clients a scoped key, or sign in for the UI, or keep the account unconfigured on an instance that is not exposed |
| 403 on a model you expected a scoped key to reach | the requested name is not in `models` and is not its base model | add the exact name, or `*`; check `GET /api/admin/keys` |
| 403 with a key that worked a minute ago | a scoped key was revoked — revocation is immediate, no reload — or the entry was removed from `apiKeys` and the config reloaded | look for `revokedAt` in `GET /api/admin/keys`; for a legacy key, check the log for the reload |
| 403 on `/metrics`, `/logs`, `/api/mcp`, other `/api/*` with a scoped key | scoped keys only reach model routes and the filtered listings | use an admin session, or a legacy key, for ops routes |
| 403 `{"error":"missing CSRF header"}` | a cookie-authenticated POST/PUT/DELETE without `X-Requested-With: llama-swap` | add the header; API-key requests never need it |
| 401 from `POST /api/admin/setup` | the token is wrong, or it is from a previous run | restart and copy the current token from the log |
| 409 from `POST /api/admin/setup` | an admin account already exists | sign in, or `-reset-admin` if the password is lost |
| 429 with `Retry-After` | throttle: five failed attempts from one IP lock it for a minute, doubling to 15 minutes; 100 failures across all IPs lock everything for a minute | wait it out |
| a selector or pin name is 403 for a key that lists its target | the check uses the name the client sent, before rewriting | list the name the client sends (the selector or pin), not its target |
| 2FA codes are rejected and the phone is gone | the authenticator, not llama-swap, holds the secret | use a recovery code; if those are gone, `-reset-admin` |
| setup is required on every start | the auth database could not be opened, so state is in memory | fix the path or its permissions; the warning in the log says which file failed |

## Keep the keys out of the file

Use env macros so the config itself is safe to commit:

```yaml
apiKeys:
  - "${env.LLAMA_SWAP_KEY}"
  - "${env.LLAMA_SWAP_KEY_ROTATE}"
```

`${env.VAR}` is substituted before anything else. **If the variable is not set,
config loading fails with an error** — which is what you want. A typo becomes a
startup failure rather than an instance running with an empty key list.

The same applies anywhere a secret appears:

```yaml
peers:
  openrouter:
    proxy: https://openrouter.ai/api
    apiKey: ${env.OPENROUTER_API_KEY}
    models: [z-ai/glm-4.7, moonshotai/kimi-k2-0905]

models:
  hf-model:
    env:
      - "HF_TOKEN=${env.HF_TOKEN}"
    cmd: llama-server --port ${PORT} -hf some/repo
```

## How peer keys are used

`peers.*.apiKey` is injected into outgoing requests to that peer, as **both**
`Authorization: Bearer <key>` and `x-api-key: <key>`. Leave it blank and no key
is added. It accepts a macro, so use `${env.*}`.

This is the key llama-swap presents *to* the peer. It is unrelated to the
`apiKeys` clients present *to* llama-swap, and to the scoped keys above.

## What ends up in your config file

Worth being aware of, because config files get pasted into issues and chats:

- `apiKeys` — literal keys, unless you used env macros
- `peers.*.apiKey` — literal peer keys, same
- `models.*.env` — literal `NAME=value` pairs
- `models.*.cmd` / `cmdStop` — full command lines, which often carry
  `--api-key` flags and absolute paths
- `macros` — **after** env substitution has already run

That last one is the non-obvious one: a macro like
`llama: "llama-server --api-key ${env.KEY}"` holds the resolved secret once the
config is loaded.

Before sharing a config, strip those. Redact rather than delete so the shape of
the problem survives. The admin account, sessions, recovery codes and scoped
keys are not in the config at all — but the auth database holds them, so treat
that file as a secret too.

## Related

- `reference/config/apiKeys`, `reference/config/peers`
- `guides/configuration/macros` — how env macros resolve
- `guides/routing/profiles-and-selectors` — what profiles and selectors do to a model name
- `guides/api-integration/mcp-endpoint` — the MCP endpoint is an ops route, so scoped keys cannot reach it

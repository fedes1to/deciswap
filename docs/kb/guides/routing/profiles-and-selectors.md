---
title: Profiles and selectors
summary: Switch model sets at runtime with profiles, or resolve a virtual model ID per request with selectors, including automatic routing by a decision model.
category: guides
tags: [profiles, selectors, aliases, routing, virtual-models, decision, auto-routing]
config_keys: [profiles, profiles.*.pins, selectors, selectors.*.strategy, selectors.*.targets, selectors.*.routes, selectors.*.settings.decider, selectors.*.settings.default, selectors.*.settings.minConfidence, selectors.*.settings.timeout, selectors.*.settings.mode, selectors.*.settings.sessionHeader, selectors.*.settings.sessionTTL, selectors.*.settings.refreshHeader, selectors.*.settings.unloadDecider, selectors.*.settings.switchMargin, selectors.*.settings.switchConfidence, selectors.*.settings.minDwell, selectors.*.settings.context]
updated: 2026-08-25
---

# Profiles and selectors

Both let a client ask for a stable model name and get a different real model
behind it. They solve different problems.

## Profiles: switch a whole set at once

A profile is a named set of model ID replacements. One profile, or none, is
active at a time, and you switch it from the web UI or the API.

```yaml
profiles:
  coding:
    description: "Coding-focused model routing"
    pins:
      llm-code: "qwen3-coder-30b"
      llm-plan: "gpt-oss-120b"
      image-gen: ~

  writing:
    description: "Long-form writing"
    pins:
      llm-code: "gemma-3-27b"
      llm-plan: "gemma-3-27b"
```

Your editor always requests `llm-code`. Flip the active profile and it gets a
different model, with no client-side change.

- Pin targets may be a local model, a fully qualified peer model, an alias, a
  `setParamsByID` alias, or a selector.
- An empty string or YAML `~` **disables** the pin — requests get a 404 and it
  is left out of model listings. Existing local, alias and peer IDs stay
  reachable.
- Pins are applied first, before aliases, filters and routing.
- Startup and config reload select **no** profile.

Switch at runtime:

```console
$ curl http://localhost:8080/api/profiles
$ curl -X PUT http://localhost:8080/api/profiles/active -d '{"name":"coding"}'
$ curl -X PUT http://localhost:8080/api/profiles/active -d '{"name":null}'
```

## Selectors: resolve per request

A selector is a virtual model ID resolved to a concrete target *on every
request*, based on a strategy.

```yaml
selectors:
  coding-model:
    strategy: warm
    targets: [gpt-oss-120b, qwen3-coder-30b]
    name: "Coding Model"
    description: "Best currently loaded coding model"
```

Three strategies:

**`warm`** — use the first target that is already ready; failing that, one that
is already starting; failing that, cold-start the first. This is the one you
want for "just use whatever is already loaded and don't make me wait".

**`pin`** — always the first target. The remaining entries are reference data
only. Useful as a stable indirection you can edit in one place.

**`spillover`** — fill each target up to a reservation count, then move to the
next. Good for spreading load across local and remote capacity:

```yaml
selectors:
  llama:
    strategy: spillover
    targets: [local-llama, peer-a/llama, peer-b/llama]
    settings:
      spillover: 4     # requests per active target before spilling over
```

Rules worth knowing:

- Selector IDs cannot collide with model IDs, aliases or fully qualified peer
  model names.
- A selector's targets cannot be other selectors.
- Selectors are **not** supported on `/upstream/<model>` paths.
- Profiles run first, so a profile pin may target a selector.

### Decision: let a model pick the specialist

`decision` asks a decider model which specialist should handle each request,
then forwards the request to that model. Clients call one virtual ID such as
`auto`, and you can define several (`auto`, `auto-coding`, ...) with different
choices.

```yaml
selectors:
  auto:
    strategy: decision
    name: "Auto"
    description: "Routes each request to the best model for the task"
    routes:
      general:
        model: fable-5.1
        description: "general knowledge, writing, explanations"
      coding:
        model: opus-5.5
        description: "writing, debugging, reviewing code"
      security:
        model: glm-5.3
        description: "pentesting, exploits, CVEs, hardening"
    settings:
      decider: kev            # serves /v1/systemone; local, alias or peer model
      default: fable-5.1      # fallback target, required
      minConfidence: 0.5      # optional, 0..1, default 0
      timeout: 5s             # optional, default 5s
```

Each route is a label, a target model, and a description. The decider only sees
the labels and descriptions, so the descriptions decide the quality of the
choice. `targets` is not allowed on a decision selector: llama-swap derives the
target list from `routes` and `settings.default`.

**What the decider sees.** The text of the last human user turn. Tool results
are skipped, so an agent's tool loop classifies on the same text as the request
that started it and stays on the same model. `<system-reminder>` blocks are
stripped, and long text is cut to its opening and closing parts. Embeddings,
audio, image, rerank and other non-chat routes never call the decider; they go
to `default`.

**Caching.** A decision is cached per selector and classification text for ten
minutes, up to 1024 entries, so repeated turns do not call the decider again.

**Fallback.** The request still succeeds if the decider fails. Any of these
sends the request to `default`: a decider error or non-200 response, the call
exceeding `timeout`, a malformed answer, an unknown label, or a confidence below
`minConfidence`. The response headers say which happened:

- `X-Routed-Model` — the model the request was sent to
- `X-Routed-Label` — the chosen label, or `default`
- `X-Routed-Reason` — present only on fallback (`timeout`, `low-confidence`, ...)

### Session modes

By default every request is decided on its own. Two stateful modes keep a
conversation on one model instead. Both need a stable session key in a request
header; `X-Session-ID` is the default, and requests without it fall back to
per-request decisions.

**`sticky`** asks the decider only on the first request of a session, then
routes every later turn to the same target. Set `unloadDecider: true` to stop
the decider process after that first decision; the next new session starts it
again. A client can force a new decision with the refresh header
(`X-Route-Refresh: 1`) or by calling the API:

```console
$ curl -X POST http://localhost:8080/api/decision/refresh     -H 'Content-Type: application/json' \
    -d '{"selector":"auto","session":"chat-42"}'
```

Omitting `session` drops every session of that selector.

**`adaptive`** asks the decider every turn, but keeps the current target unless
the new route wins by `switchMargin` and reaches `switchConfidence`, and the
session has been on its current target for at least `minDwell`. The guardrails
matter: each switch discards the target's prefix cache and can cold-start a
multi-gigabyte model, so a single borderline turn must not move the session.
The decider stays resident in this mode.

```yaml
selectors:
  auto:
    strategy: decision
    routes:
      fast:     {model: lfm, description: "short, easy, latency matters"}
      decisive: {model: swift, description: "hard reasoning and math"}
      smart:    {model: flash, description: "code and long documents"}
      general:  {model: int5, description: "everything else"}
    settings:
      decider: d1
      default: int5
      mode: adaptive           # or sticky
      sessionHeader: X-Session-ID
      sessionTTL: 1h
      switchMargin: 0.25       # adaptive only
      switchConfidence: 0.6    # adaptive only
      minDwell: 2m             # adaptive only
      # unloadDecider: true    # sticky only
```

A selector advertises the **smallest** context among its targets by default,
so a virtual ID never promises more room than the model it may pick. Set
`settings.context` to advertise a fixed value instead.

Each routed response carries `X-Routed-Session`: `decided` for the first
decision, `pinned` when the session reused its target, `switched` when
adaptive moved it, and `unchanged` when a decider failure left it in place.

Only one large target fits on two GPUs, so sessions pinned to different models
will still evict each other; the pin survives, the reload does not.

**What goes wrong.**

- *The decider gets swapped out.* The decider is an ordinary model, so a swap
  can unload a target to make room, and a decision then waits for a cold start
  inside `timeout`. Most requests fall back to `default` in that case. Keep the
  decider resident with a [matrix or group](groups-and-matrix.md), or use a tiny
  CPU model such as Julia-1 or Laya so it never competes for VRAM.
- *Vague labels pick the wrong model.* A route described as "general" will
  attract anything. Write descriptions that say when the route applies and when
  it does not, and keep the labels distinct.
- *Low-confidence answers fall back silently.* Check `X-Routed-Reason` before
  raising `minConfidence`; a high threshold sends most traffic to `default`.

Set `settings.instructions` to change the question the decider is asked. The
default is "Which specialist should handle this request?".

## Which one?

**Profiles** are a mode switch you make deliberately — "I'm coding now". They
change several mappings at once and persist until you change them again.

**Selectors** are automatic, per request, and based on current state. Nobody
flips them.

They compose: pin `llm-code` in the `coding` profile to a `warm` selector, and
you get "when I'm coding, use whichever coding model is already hot".

## Related

- `reference/config/profiles` and `reference/config/selectors`
- `guides/routing/groups-and-matrix` — which models can be loaded at the same time

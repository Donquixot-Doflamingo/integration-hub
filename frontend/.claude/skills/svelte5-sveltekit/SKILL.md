---
name: svelte5-sveltekit
description: Svelte 5 (runes) and SvelteKit 2 conventions for the Integration Hub admin console in frontend/. Covers $state/$derived/$effect/$props/$bindable, snippets instead of slots, routing and load functions, form actions, hooks.server.ts auth, error/redirect, $env modules, SSR choices and this repo's folder layout. Use whenever you create or edit a .svelte, .svelte.ts, +page/+layout/+server file or hooks in frontend/, add a route or screen, fix reactivity bugs, or see Svelte 4 syntax (export let, $:, on:click, slot elements, createEventDispatcher) that must be migrated.
---

# Svelte 5 + SvelteKit 2 for the admin console

The console in `frontend/` is SvelteKit 2 + Svelte 5 (runes mode) + TypeScript + Vite. It is an internal
operator tool for the Hub control plane and talks **only** to `/admin/v1` through the generated client
(see the `api-client-from-contract` skill). Write Svelte 5 only: no Svelte 4 syntax in new code.

## Project layout (follow it; do not invent new top-level folders)

```text
frontend/src/
  app.d.ts                 # App.Locals (user, roles, accessToken), App.Error (code, requestId)
  hooks.server.ts          # handle: OIDC session -> event.locals; handleError
  lib/
    api/                   # schema.d.ts (GENERATED), client.ts (wrapper), errors.ts
    components/            # reusable UI: ConfirmDialog, DataTable, Pager, StatusBadge, DiffView, CopyOnce
    stores/                # *.svelte.ts shared reactive state (session, toasts, prefs)
    utils/                 # pure helpers (formatting, query-string <-> filter mapping)
  routes/
    (auth)/login/          # OIDC dev stub entry
    (app)/+layout.server.ts   # requires a session; loads /me
    (app)/+layout.svelte      # shell: nav, toasts, focus management
    (app)/integrations/       # list, [id]/ (versions, diff, publish, rollback, weights, pause)
    (app)/health/             # vendor/account breaker board
    (app)/requests/           # explorer, [requestId]/ timeline (unseal)
    (app)/dlq/                # list + replay by filter
    (app)/transformers/       # viewer, dry-run, goldens
    (app)/tenants/ api-keys/ registrar-endpoints/ change-log/ me/
```

Route groups `(app)` / `(auth)` do not appear in URLs. Co-locate route-only components next to the
`+page.svelte`; move them to `$lib/components` once a second route uses them.

## Runes cheat sheet

```svelte
<script lang="ts">
  import type { Snippet } from 'svelte';
  let { integration, onPause, children }: {
    integration: Integration; onPause?: (reason: string) => void; children?: Snippet;
  } = $props();

  let filter = $state('');                                   // local mutable state
  const visible = $derived(integration.versions.filter(v => v.reason.includes(filter)));
  const total = $derived.by(() => visible.reduce((n, v) => n + v.weight, 0)); // multi-line derivation
</script>

<input bind:value={filter} aria-label="Filter versions" />
<button onclick={() => onPause?.('maintenance')}>Pause</button>
{@render children?.()}
```

- `$state` for values the component mutates. Objects/arrays are deeply reactive proxies; use
  `$state.raw` for large read-only data you replace wholesale (e.g. a page of 500 requests).
- `$derived` for everything computable from other state. Never mirror props into `$state` + `$effect`.
- `$props()` replaces `export let`. Rename reserved words via destructuring (`class: klass`).
- `$bindable()` only for genuine two-way inputs (custom form controls): `let { value = $bindable('') } = $props();`.
- Callback props (`onconfirm`, `oncancel`) replace `createEventDispatcher`.
- Shared state lives in `$lib/stores/*.svelte.ts` as exported objects/classes with `$state` fields.
  Export an object or getter, not a reassigned `let` (importers would not see reassignment).

### When NOT to use `$effect`
`$effect` is for syncing with the outside world (DOM APIs, focus, a chart lib, `EventSource`, timers).
It is the wrong tool for:
- Computing values -> `$derived`.
- Fetching page data -> `load` in `+page.ts`.
- Reacting to a user action -> do it in the event handler.
- Writing state that another effect reads (loops, flicker).

Always return a cleanup when you subscribe or start a timer:
```ts
$effect(() => {
  const id = setInterval(refreshBreakers, 5000);
  return () => clearInterval(id);
});
```
Use `untrack(() => ...)` when reading a value inside an effect must not create a dependency.

## Snippets, not slots

```svelte
<!-- DataTable.svelte -->
<script lang="ts" generics="T">
  import type { Snippet } from 'svelte';
  let { rows, row, empty }: { rows: T[]; row: Snippet<[T]>; empty?: Snippet } = $props();
</script>
{#if rows.length}{#each rows as r (r)}{@render row(r)}{/each}{:else}{@render empty?.()}{/if}

<!-- usage -->
<DataTable rows={data.items}>
  {#snippet row(i)}<tr><td>{i.id}</td><td>{i.status}</td></tr>{/snippet}
  {#snippet empty()}<p>No integrations match these filters.</p>{/snippet}
</DataTable>
```
Content between component tags becomes the `children` snippet.

## SvelteKit data and mutations

- **Reads**: `+page.ts` / `+layout.ts` `load` (runs on server first render, then in the browser).
  Use the `fetch` passed to `load` (it forwards cookies and dedupes SSR requests) when building the client.
- **Server-only reads** (need the access token or secrets): `+page.server.ts` / `+layout.server.ts`.
- **Mutations** (publish, rollback, pause, replay, unseal): form actions in `+page.server.ts` with
  `use:enhance`, so they work without JS and keep the token server-side. Return `fail(400, {...})` for
  validation errors; return the resulting `change_log_id` on success.

```ts
// routes/(app)/integrations/[id]/+page.server.ts
import { fail, error } from '@sveltejs/kit';
import { adminClient } from '$lib/api/client';
export const actions = {
  pause: async ({ request, params, locals, fetch }) => {
    const reason = String((await request.formData()).get('reason') ?? '').trim();
    if (!reason) return fail(400, { reason, missing: 'reason' });
    const api = adminClient({ fetch, token: locals.accessToken });
    const res = await api.call(c => c.POST('/admin/v1/integrations/{id}/pause',
      { params: { path: { id: params.id } }, body: { reason } }));
    if (!res.ok) return fail(res.status, { error: res.error });
    return { changeLogId: res.data.change_log_id };
  }
};
```
(Path and body names must come from the generated `schema.d.ts`; the above is the shape, check the spec.)

- `error(404, {...})` and `redirect(303, '/login')` are **thrown helpers** in SvelteKit 2: call them,
  do not `throw error(...)` manually wrapped in try/catch that swallows them.
- `+error.svelte` per group renders `page.error` (`code`, `requestId`); never show stack traces.
- Read page state with `import { page } from '$app/state'` (Svelte 5), not the `$app/stores` store.

## Auth (HUB-253): session hook, layout guard, role gating
How it is wired in this repo (do not add a second mechanism):
- `src/lib/server/auth/`: `AuthProvider` interface with `devProvider` (role picker; mock builds or
  `PUBLIC_AUTH_MODE=dev`) and `oidcProvider` (placeholder until HUB-282). `authProvider()` picks one.
- Session = one HMAC-signed, **httpOnly**, SameSite=Lax cookie (`hub_console_session`), set only by the
  login form action. Never `localStorage`, never a token in page data: pages get `PublicSession`.
- `hooks.server.ts`: `sequence(rejectDoubleSlashPaths, authenticate)` puts the verified session in
  `event.locals.session`; `handleFetch` adds the bearer token to server-side admin API fetches.
- The redirect to `/login?next=...` lives in `routes/(app)/+layout.server.ts` (works for SSR and for
  client navigations, unlike a redirect thrown in `handle`). `next` always goes through `safeNext()`.
- `routes/(app)/+layout.ts` awaits `/me` and returns `access` (`$lib/auth/roles`). Gate controls with
  `<RoleGate access={data.access} min="editor">`; `access` fails closed when `/me` failed. The API's 403
  stays the truth.
```ts
// any page under (app): data.access comes from the layout
<RoleGate access={data.access}>
  <button onclick={openPublish}>Publish</button>
  {#snippet denied(role)}<p>Read only: publishing needs the {role} role.</p>{/snippet}
</RoleGate>
```

## Environment variables
- `$env/static/public` (`PUBLIC_*`, inlined at build) for things like `PUBLIC_HUB_ADMIN_BASE`.
- `$env/dynamic/private` / `$env/static/private` for OIDC client secret, only in `*.server.ts`.
- Never import private env into a universal `+page.ts` or a component; the build fails, and that is good.

## SSR vs CSR
Internal console: keep SSR on (fast first paint, form actions, session cookie) and keep pages
**prerender = false**. Opt a page out with `export const ssr = false` only for browser-only libs
(e.g. a diff editor) and say why in a comment. No secrets or per-user data in prerendered output.

## Svelte 4 -> 5 pitfalls (reject these in review)
| Svelte 4 | Svelte 5 |
| --- | --- |
| `export let x` | `let { x } = $props()` |
| `$: y = x * 2` | `const y = $derived(x * 2)` |
| `$: { sideEffect() }` | `$effect(() => sideEffect())`, only for external sync |
| `on:click={f}` | `onclick={f}` (no `|preventDefault` modifier: call `e.preventDefault()`) |
| `<slot />`, `<slot name="x">` | `{@render children?.()}`, snippet props |
| `createEventDispatcher` | callback props |
| `import { page } from '$app/stores'` + `$page` | `import { page } from '$app/state'` + `page` |
| writable stores for local state | `$state` (stores still work, but do not add new ones) |

Other traps: destructuring a `$state` object loses reactivity (`const { a } = obj`); keyed `{#each}`
needs a stable key (`(item.id)`); `$derived` must be pure (no mutation, no fetch).

## Before you finish
- [ ] `npm run check` (svelte-check + tsc) is clean, `npm test` green.
- [ ] No hand-written API types; imports come from `$lib/api`.
- [ ] Destructive actions follow the `admin-console-ux` skill (confirm + reason + change_log id).

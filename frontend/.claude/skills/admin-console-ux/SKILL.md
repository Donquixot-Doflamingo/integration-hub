---
name: admin-console-ux
description: UX rules for the Integration Hub admin console (control plane). Destructive or irreversible actions (publish, rollback, pause, retire, purge DLQ, force breaker, unseal) need a confirm dialog with a mandatory reason and must show the resulting change_log id; side-by-side version diffs; secrets never displayed (API key shown once with copy); breaker states conveyed by text/icon plus colour; cursor-paginated tables with filters in the URL; loading/empty/error states; keyboard, focus, labels, contrast; dense-but-readable data layout. Use whenever you design or build any screen, dialog, table, status badge or form in frontend/, review UI code, or decide how an operator action should behave.
---

# Admin console UX

Users are platform engineers and on-call operators acting on live traffic (DESIGN §16.1). The UI's job
is to make the current state obvious and make dangerous changes deliberate and traceable.
Visual direction (palette, type, layout personality) comes from the vendored **`frontend-design`**
skill; this skill sets the behavioural rules, which win over aesthetics when they conflict.

## 1. Dangerous actions: confirm + reason + change_log id
Applies to: **publish, rollback, pause/resume, set weights, retire account, purge DLQ, DLQ replay,
force breaker open/close, unseal, rotate/promote credentials, revoke API key**.

Use one shared `ConfirmDialog` (`$lib/components/ConfirmDialog.svelte`):
- Title states the exact action and object: "Roll back int_evt_brandA to v7".
- Body states the consequence in plain words, with numbers where known: "Creates v9 with v7's content.
  Parked requests re-resolve to v9." / "Replays 1,284 dead letters matching these filters."
- **Reason** textarea, required, labelled, trimmed, min length (e.g. 10 chars). Confirm is disabled
  until valid. The reason is sent to the API (it lands in `change_log`).
- For the most destructive (purge DLQ, retire, force breaker open in prod) also require typing the
  object id to confirm.
- Confirm button label repeats the verb ("Roll back"), never "OK"/"Yes". Cancel is the default focus.
- While submitting: disable both buttons, show progress; never allow a double submit.
- On success: close, show a persistent toast/inline notice with the **change_log id** (link to the
  change log entry) and what changed. On error: keep the dialog open with reason intact, show
  `error.code`, message and **request id**.
- Role-gated: hide for `viewer`/`tenant_viewer`, but still handle a 403 gracefully.

## 2. Versions and diffs
- Version list shows: version, active marker (text "Active", not just a dot), author, reason,
  published at, weights.
- Diffs are **side by side** (old left, new right) with structural YAML/JSON diff, changed lines
  marked by symbol (+/-) and colour, collapsible unchanged sections, and a summary line
  ("3 fields changed: targets[0].account, delivery.wait_ms, webhooks").
- Publish flow always shows the diff against the active version before the confirm dialog.
- Rollback = new version with old content: say so in the UI; never imply history is deleted.
- Weights editor: values must sum to 100, show the sum live, block publish otherwise.

## 3. Secrets: never displayed
- Credentials, signing secrets, unsealed-but-not-requested payloads: never rendered, never in the DOM,
  never in `console.log`, never in URLs or local storage.
- **API key creation**: show the key **once** in a read-only field with a Copy button and a clear
  "You will not see this again" notice; on close, discard it from state. Afterwards show only prefix,
  created at, last used, scopes.
- Copy-to-clipboard: announce "Copied" via an `aria-live` region.
- Sealed audit payloads show size + SHA-256 and an "Unseal" action (confirm + reason, §1).
- Masked fields render as the masked value the API returns; never try to unmask client-side.

## 4. Status: breaker states and health
| State | Text | Icon | Colour token |
| --- | --- | --- | --- |
| closed | "Closed" | check | `--status-ok` |
| half-open | "Half-open" | half circle / probe | `--status-warn` |
| open | "Open", plus "retry in 23 s" | x / stop | `--status-danger` |
| paused (integration) | "Paused" | pause | `--status-neutral` |
State is **always text + icon**; colour is a third cue, never the only one (WCAG 1.4.1).
Health board groups by vendor -> account -> endpoint; show failure rate, open since, parked count,
oldest parked age. Sort worst first. Auto-refresh with a visible "Updated 5 s ago" and a pause toggle.

## 5. Tables: pagination and filters
- **Cursor pagination** (whatever cursor params the contract defines): Next / Previous, page size
  selector; no "page 7 of 92" totals unless the API returns them.
- **Filters live in the URL query** (`?tenant=orders-service&status=dead_lettered&since=...`) so views
  are shareable and survive reload; the `load` function reads them. Changing a filter resets the cursor.
- Show active filters as removable chips plus "Clear all".
- Ids (request id, integration id, change_log id) are monospace, copyable, and link to their detail.
- Timestamps: relative ("3 min ago") with absolute UTC + local in a `title`/tooltip; one format app-wide.
- Numbers right-aligned with thousands separators; durations with units (ms, s).

## 6. Loading, empty, error
Every data view handles all four states:
- **Loading**: skeleton rows matching the layout (no layout jump); keep previous data while refetching.
- **Empty**: say why and what to do ("No dead letters for these filters. Clear filters").
  Distinguish "nothing exists" from "filters hide everything".
- **Error**: canonical `error.code`, human message, **request id** with copy, Retry button.
- **Partial**: e.g. breaker state stale -> show data with a "stale" notice, not an error page.

## 7. Accessibility (non-negotiable)
- Everything works by keyboard: visible focus ring, logical tab order, no keyboard traps except
  inside open dialogs (focus trapped, Esc closes, focus returns to the trigger).
- Native elements first (`<button>`, `<a>`, `<table>`, `<dialog>`/proper `role="dialog"` with
  `aria-modal`, `aria-labelledby`). Icon-only buttons have `aria-label`.
- Every input has a `<label>`; errors linked with `aria-describedby`; required marked in text.
- Contrast: 4.5:1 for text, 3:1 for UI components and focus indicators, in light and dark themes.
- Tables have `<th scope>`; sortable columns expose `aria-sort`.
- Live updates (toasts, auto-refreshed counts) via polite `aria-live`; do not steal focus.
- Respect `prefers-reduced-motion`.

## 8. Layout: dense but readable
- Operators scan many rows: compact row height (about 32-36 px), but never smaller than 14 px text for
  data, 12 px for secondary metadata.
- One primary action per view, placed consistently (top right of the object header); destructive
  actions visually separated (e.g. in a "Danger" group at the end), never next to "Save".
- Detail pages: header (id, status text, owner tenant, active version) -> tabs (Versions, Diff,
  Weights, Webhooks, Requests, Change log).
- Never show vendor/account internals to `tenant_viewer` beyond what the API returns for that role.
- Wide tables scroll horizontally inside their container with a sticky first column; the page never
  scrolls sideways.

## Review checklist
- [ ] Every dangerous action uses `ConfirmDialog` with required reason and shows change_log id.
- [ ] No secret in DOM, logs, URL or storage; API key shown once.
- [ ] Status conveyed by text + icon; contrast checked.
- [ ] Filters in URL; cursor pagination; four data states handled; request id on errors.
- [ ] Keyboard and axe checks pass (see `frontend-testing`).

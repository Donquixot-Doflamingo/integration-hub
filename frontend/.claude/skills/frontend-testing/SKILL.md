---
name: frontend-testing
description: Testing the Integration Hub admin console. Vitest + @testing-library/svelte for components and loads, MSW (handlers built from the OpenAPI examples) for API, Playwright e2e for critical operator flows (publish with reason, rollback, DLQ replay by filter, unseal with reason, API key shown once), axe accessibility checks, and screenshot consistency. Use whenever you write or fix tests in frontend/, add a screen or destructive action, touch a critical flow, debug a flaky test, or need to decide between unit, component and e2e tests. Explains how this relates to the vendored webapp-testing skill.
---

# Frontend testing

Stack: **Vitest** (jsdom) + **@testing-library/svelte** + **@testing-library/user-event** +
**MSW** for unit/component tests; **Playwright** (TypeScript, `@playwright/test`) for e2e;
**axe** (`@axe-core/playwright`, and `vitest-axe`/`axe-core` in component tests) for accessibility.
Never call a real Hub or vendor from tests; MSW (or the dev stack with `mockvendor`) only.

## What to test where
| Layer | Tool | Covers |
| --- | --- | --- |
| Pure utils (`$lib/utils`, filter <-> query mapping, diff building) | Vitest | table-driven, no DOM |
| Components (`ConfirmDialog`, `DataTable`, `StatusBadge`, `DiffView`, `CopyOnce`) | Vitest + Testing Library | rendering states, keyboard, callbacks, a11y |
| `load` / form actions | Vitest, call the function with a fake event + MSW | error mapping, `fail()` on missing reason |
| Screens + flows | Playwright against `npm run dev:mock` (MSW browser) or the dev stack | critical flows below |

Prefer many fast component tests and few e2e tests for flows that would hurt if broken.

## Commands
```bash
cd frontend
npm run check          # svelte-check + tsc
npm test               # vitest run
npm run test:watch
npx playwright test    # e2e (starts the app via playwright.config webServer)
npx playwright test --ui
```

## Component test pattern
```ts
import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';

it('requires a reason before confirming', async () => {
  const onconfirm = vi.fn();
  render(ConfirmDialog, { props: { open: true, title: 'Roll back int_evt_brandA', onconfirm } });
  const confirm = screen.getByRole('button', { name: /roll back/i });
  expect(confirm).toBeDisabled();
  await userEvent.type(screen.getByLabelText(/reason/i), 'v8 drops campaign field');
  await userEvent.click(confirm);
  expect(onconfirm).toHaveBeenCalledWith({ reason: 'v8 drops campaign field' });
});
```
- Query by role/label/text (what an operator perceives), not by class or test id. Use `data-testid`
  only for things without an accessible name (rare).
- Svelte 5: pass props via `render(Component, { props })`; for snippets, render a small wrapper
  test component.
- Assert all four states of data views: loading, empty, error (shows request id), populated.

## MSW setup
```ts
// vitest.setup.ts
import { server } from '$lib/api/mocks/server';
beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => server.resetHandlers());
afterAll(() => server.close());
```
- `onUnhandledRequest: 'error'` so a call to an unmocked (maybe non-contract) endpoint fails loudly.
- Override per test with `server.use(http.post('*/admin/v1/...', () => HttpResponse.json(envelope, { status: 403 })))`.
- Fixtures come from contract examples (see `api-client-from-contract`); never hand-craft new fields.

## Critical e2e flows (must exist and stay green)
Each flow asserts: confirm dialog opens, confirm is blocked without a reason, the request body sent
contains the reason, success shows the **change_log id**, and focus returns to a sensible place.
1. **Publish with reason**: integration detail -> new version -> side-by-side diff -> Publish -> reason -> change_log id shown, active version updated.
2. **Rollback**: pick older version -> Roll back -> reason -> new version created (v9 = v7 content) with change_log id.
3. **DLQ replay by filter**: set filters (tenant, integration, reason, time range) -> URL query updated -> Replay -> dialog shows the count that will be replayed -> reason -> result count + change_log id.
4. **Unseal with reason**: request timeline -> sealed event -> Unseal -> reason -> payload visible, audit notice shown.
5. **API key shown once**: create key -> secret visible once with copy button -> after closing/navigating it is never shown again.
6. Pause/resume integration and force breaker follow the same pattern (add when the screen exists).

```ts
test('rollback requires a reason and shows change log id', async ({ page }) => {
  await page.goto('/integrations/int_evt_brandA');
  await page.getByRole('button', { name: 'Roll back to v7' }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog.getByRole('button', { name: /roll back/i })).toBeDisabled();
  await dialog.getByLabel('Reason').fill('v8 broke campaign mapping');
  const req = page.waitForRequest(r => r.method() === 'POST' && r.url().includes('/rollback'));
  await dialog.getByRole('button', { name: /roll back/i }).click();
  expect((await req).postDataJSON()).toMatchObject({ reason: 'v8 broke campaign mapping' });
  await expect(page.getByText(/change log/i)).toContainText(/chg_/);   // id format per contract example
});
```
Use web-first assertions (`await expect(locator)...`), never fixed `waitForTimeout`.

## Accessibility checks
- Every e2e screen visit runs axe and fails on `serious`/`critical` violations:
```ts
import AxeBuilder from '@axe-core/playwright';
const results = await new AxeBuilder({ page }).analyze();
expect(results.violations.filter(v => ['serious','critical'].includes(v.impact ?? ''))).toEqual([]);
```
- Keyboard-only test for dialogs: Tab cycles inside, Esc closes, focus returns to the trigger.
- Breaker badges: assert the text ("Open", "Half-open", "Closed") exists, not only a colour.

## Visual consistency
- Playwright `await expect(page).toHaveScreenshot()` for a handful of stable screens (health board,
  diff view, DLQ table) with MSW fixtures and a frozen clock; mask timestamps and ids.
- Update baselines only intentionally (`npx playwright test --update-snapshots`) and review the diff in the PR.
- Visual direction comes from the `frontend-design` skill; tests only guard against regressions.

## Relation to the vendored `webapp-testing` skill
`webapp-testing` (vendored, Python Playwright + `scripts/with_server.py`) is for **ad-hoc exploration**:
reconnaissance of a running app, screenshots, reading console logs while debugging. Use it to look
around or reproduce a bug. Tests that live in the repo and run in CI are **TypeScript Playwright** in
`frontend/e2e/` as described here. Do not commit Python Playwright scripts into `frontend/`.

## Flakiness rules
- No real network, no real clock dependence: `vi.useFakeTimers()` / Playwright `page.clock`.
- Each test sets up its own MSW overrides; reset after each.
- A flaky test gets a `todo` ticket and a fix, not a retry loop.

## Done
- [ ] `npm run check && npm run lint && npm test` green.
- [ ] Touched critical flows green in Playwright, axe clean (no serious/critical).
- [ ] New component has state tests (loading/empty/error/populated) and a keyboard test if interactive.

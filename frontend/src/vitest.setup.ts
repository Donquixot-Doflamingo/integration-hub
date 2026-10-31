import '@testing-library/jest-dom/vitest';
import { afterAll, afterEach, beforeAll, vi } from 'vitest';
import { server } from '$lib/api/mocks/server';

// SvelteKit's runtime env is not populated under Vitest; tests use the default admin API base.
vi.mock('$env/dynamic/public', () => ({ env: {} }));
vi.mock('$env/dynamic/private', () => ({ env: {} }));
// `use:enhance` needs SvelteKit's client runtime; tests run the route's real form actions instead.
vi.mock('$app/forms', () => import('$lib/testing/forms'));

// Every test runs against MSW built from the contract examples. Unmocked requests fail loudly.
beforeAll(() => server.listen({ onUnhandledFrame: 'error' }));
afterEach(() => server.resetHandlers());
afterAll(() => server.close());

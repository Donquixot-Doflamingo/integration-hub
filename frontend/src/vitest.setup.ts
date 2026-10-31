import '@testing-library/jest-dom/vitest';
import { afterAll, afterEach, beforeAll, vi } from 'vitest';
import { server } from '$lib/api/mocks/server';

// SvelteKit's runtime env is not populated under Vitest; tests use the default admin API base.
vi.mock('$env/dynamic/public', () => ({ env: {} }));

// Every test runs against MSW built from the contract examples. Unmocked requests fail loudly.
beforeAll(() => server.listen({ onUnhandledFrame: 'error' }));
afterEach(() => server.resetHandlers());
afterAll(() => server.close());

import { setupWorker, type SetupWorker } from 'msw/browser';
import type { SessionUser } from '../types';
import { createHandlers, mockMe, mockOperation } from './handlers';

const LATENCY_MS = 300;
let worker: SetupWorker | undefined;
let identityKey: string | undefined;

/**
 * Starts MSW in the browser for `npm run dev:mock` (no backend). The worker script is served by the
 * `msw/vite` plugin (worker-only mode), which vite.config.ts only installs when mocking is on.
 */
export async function startMockWorker(): Promise<void> {
	worker = setupWorker(...createHandlers({ latencyMs: LATENCY_MS }));
	await worker.start({
		serviceWorker: { url: '/mockServiceWorker.js' },
		// Every admin operation has a handler; assets, Vite HMR and anything else go to the network.
		onUnhandledFrame: 'bypass',
		quiet: true
	});
	console.info('[mock] MSW is serving /admin/v1 from contract examples (no backend).');
}

/** Makes `/me` answer as the dev-login identity, so role gating can be exercised on mocks. */
export function useMockIdentity(user: SessionUser): void {
	const key = JSON.stringify(user);
	if (!worker || key === identityKey) return;
	identityKey = key;
	worker.use(mockOperation('getMe', mockMe(user), { latencyMs: LATENCY_MS }));
}

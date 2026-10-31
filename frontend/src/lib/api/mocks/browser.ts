import { setupWorker } from 'msw/browser';
import { createHandlers } from './handlers';

/**
 * Starts MSW in the browser for `npm run dev:mock` (no backend). The worker script is served by the
 * `msw/vite` plugin (worker-only mode), which vite.config.ts only installs when mocking is on.
 */
export async function startMockWorker(): Promise<void> {
	const worker = setupWorker(...createHandlers({ latencyMs: 300 }));
	await worker.start({
		serviceWorker: { url: '/mockServiceWorker.js' },
		// Every admin operation has a handler; assets, Vite HMR and anything else go to the network.
		onUnhandledFrame: 'bypass',
		quiet: true
	});
	console.info('[mock] MSW is serving /admin/v1 from contract examples (no backend).');
}

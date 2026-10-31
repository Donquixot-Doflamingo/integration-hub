import type { ClientInit } from '@sveltejs/kit';

// Mock mode: start MSW before the first navigation so every load hits the contract-example mocks.
// `__API_MOCKING__` is a build-time constant, so production bundles drop this branch entirely.
export const init: ClientInit = async () => {
	if (__API_MOCKING__) {
		const { startMockWorker } = await import('$lib/api/mocks/browser');
		await startMockWorker();
	}
};

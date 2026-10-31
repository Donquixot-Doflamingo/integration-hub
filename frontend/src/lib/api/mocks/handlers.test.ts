import { describe, expect, it } from 'vitest';
import { adminClient } from '../client';
import { contractMocks } from './contract.gen';
import { mockError, mockNetworkError, mockOperation, mswPath } from './handlers';
import { server } from './server';

const api = () => adminClient({ baseUrl: 'http://hub.test/admin/v1' });

describe('mswPath', () => {
	it.each([
		['/me', '*/admin/v1/me'],
		[
			'/integrations/{integration_id}/versions/{version}',
			'*/admin/v1/integrations/:integration_id/versions/:version'
		]
	])('%s -> %s', (path, want) => expect(mswPath(path)).toBe(want));
});

describe('default handlers + adminClient', () => {
	it('serve the contract example for getMe', async () => {
		const res = await api().call((c) => c.GET('/me'));
		expect(res.ok && res.data).toEqual(contractMocks.getMe.example);
		expect(res.requestId).toMatch(/^req_/);
	});

	it('prefer /snapshots/latest over /snapshots/{snapshot_version}', async () => {
		const res = await api().call((c) => c.GET('/snapshots/latest'));
		expect(res.ok && res.data).toEqual(contractMocks.getLatestSnapshot.example);
	});
});

describe('typed overrides', () => {
	it('mockOperation returns a typed custom body', async () => {
		server.use(mockOperation('listIntegrations', { items: [], next_cursor: null }));
		const res = await api().call((c) => c.GET('/integrations'));
		expect(res.ok && res.data).toEqual({ items: [], next_cursor: null });
	});

	it('mockError returns the contract error envelope as a HubApiError', async () => {
		server.use(mockError('getMe', 401));
		const res = await api().call((c) => c.GET('/me'));
		if (res.ok) throw new Error('expected error');
		const example = contractMocks.getMe.errors['401']!.error;
		expect(res.error).toMatchObject({
			status: 401,
			code: 'UNAUTHORIZED',
			requestId: example.request_id
		});
	});

	it('mockNetworkError surfaces NETWORK_ERROR', async () => {
		server.use(mockNetworkError('getMe'));
		const res = await api().call((c) => c.GET('/me'));
		expect(res.ok).toBe(false);
		expect(!res.ok && res.error.code).toBe('NETWORK_ERROR');
	});
});

import { describe, expect, it } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import {
	createEndpointBody,
	endpointProblems,
	secretText,
	updateEndpointBody,
	urlProblem,
	valuesFromEndpoint,
	withoutSigningSecret
} from './endpoints';

// HUB-272: registrar endpoint input against RegistrarEndpointCreate / Update; secrets dropped.

const ep = contractMocks.getRegistrarEndpoint.example;

it.each([
	['https://orders.internal/hub/callbacks', null],
	['', 'Enter the callback URL.'],
	['http://orders.internal/cb', 'Use an https:// URL.'],
	['ftp://x', 'Use an https:// URL.'],
	['https://', 'Enter a valid URL, e.g. https://orders.internal/hub/callbacks.'],
	['https://exa mple.com', 'Enter a valid URL, e.g. https://orders.internal/hub/callbacks.']
])('urlProblem(%s)', (url, want) => {
	expect(urlProblem(url)).toBe(want);
});

describe('endpointProblems', () => {
	it('create checks the Slug id and the URL', () => {
		expect(
			endpointProblems({ endpoint_id: 'r', url: 'http://x', description: '' }, 'create')
		).toEqual({
			endpoint_id: expect.stringMatching(/3 to 64/),
			url: 'Use an https:// URL.'
		});
	});
	it('edit does not check the id', () => {
		expect(endpointProblems({ ...valuesFromEndpoint(ep), endpoint_id: '' }, 'edit')).toEqual({});
	});
});

it('createEndpointBody matches the contract request example', () => {
	expect(
		createEndpointBody(
			'orders-service',
			{
				endpoint_id: ' reg_orders_cb ',
				url: ' https://orders.internal/hub/callbacks ',
				description: 'Orders callbacks'
			},
			'Callbacks for async integrations'
		)
	).toEqual({
		ok: true,
		body: {
			endpoint_id: 'reg_orders_cb',
			tenant_id: 'orders-service',
			url: 'https://orders.internal/hub/callbacks',
			description: 'Orders callbacks',
			reason: 'Callbacks for async integrations'
		}
	});
});

describe('updateEndpointBody', () => {
	const before = valuesFromEndpoint(ep);
	it('sends only the new URL (contract example)', () => {
		expect(
			updateEndpointBody(
				{ ...before, url: 'https://orders.internal/hub/v2/callbacks' },
				before,
				'New callback route'
			)
		).toEqual({
			ok: true,
			body: { url: 'https://orders.internal/hub/v2/callbacks', reason: 'New callback route' }
		});
	});
	it('clears the description with null', () => {
		expect(updateEndpointBody({ ...before, description: ' ' }, before, 'r')).toEqual({
			ok: true,
			body: { description: null, reason: 'r' }
		});
	});
	it('refuses an edit without changes', () => {
		expect(updateEndpointBody(before, before, 'r')).toMatchObject({ ok: false });
	});
});

it('withoutSigningSecret drops the secret and keeps the change and endpoint', () => {
	const ok = {
		ok: true as const,
		status: 201,
		requestId: 'req_x',
		etag: null,
		data: contractMocks.createRegistrarEndpoint.example
	};
	const out = withoutSigningSecret(ok);
	expect(out).toEqual({
		...ok,
		data: {
			change_id: ok.data.change_id,
			endpoint: ok.data.endpoint
		}
	});
	expect(JSON.stringify(out)).not.toContain('whsec_');
});

it('secretText: version, and the overlap while two secrets sign', () => {
	expect(secretText(ep)).toBe('Version 1');
	expect(secretText(contractMocks.rotateRegistrarSecret.example.endpoint)).toBe(
		'Version 2; callbacks are also signed with version 1 until 2026-11-01 10:00 UTC'
	);
});

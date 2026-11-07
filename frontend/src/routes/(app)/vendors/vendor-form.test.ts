import { describe, expect, it } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import {
	breakerText,
	createBody,
	emptyValues,
	hasChanges,
	NAMES,
	problems,
	updateBody,
	valuesFromForm,
	valuesFromVendor,
	type FormValues
} from './vendor-form';
import {
	createEndpointBody,
	emptyEndpoint,
	endpointProblems,
	endpointValuesFromForm,
	pathParams,
	pathSchemaFor,
	latestByName,
	sortEndpoints,
	valuesFromEndpoint
} from './endpoint-form';

const vendor = contractMocks.getVendor.example;

const filled = (over: Partial<FormValues> = {}): FormValues => ({
	...emptyValues(),
	vendor_id: 'acme',
	display_name: 'Acme',
	regions: [{ code: 'in', url: 'https://in.api.acme.example' }],
	...over
});

describe('vendor form problems', () => {
	it.each<[string, Partial<FormValues>, string, RegExp]>([
		['missing id', { vendor_id: '' }, '/vendor_id', /Enter a vendor id/],
		['bad id', { vendor_id: 'a b' }, '/vendor_id', /3 to 64 characters/],
		['no name', { display_name: ' ' }, '/display_name', /Enter a display name/],
		['no region', { regions: [{ code: '', url: '' }] }, '/regions', /at least one region/],
		[
			'duplicate region',
			{
				regions: [
					{ code: 'in', url: 'https://a.example' },
					{ code: 'in', url: 'https://b.example' }
				]
			},
			'/regions',
			/listed twice/
		],
		['url not absolute', { regions: [{ code: 'in', url: '/x' }] }, '/regions', /absolute http/],
		['docs url', { docs_url: 'docs' }, '/docs_url', /absolute http/],
		['rps', { per_account_rps: '-1' }, '/defaults/rate_limit/per_account_rps', /0 or more/],
		[
			'breaker minimum',
			{ breaker: { ...emptyValues().breaker, window_ms: '999' } },
			'/defaults/breaker/window_ms',
			/at least 1,000/
		],
		[
			'ratio above 1',
			{ breaker: { ...emptyValues().breaker, failure_ratio: '1.5' } },
			'/defaults/breaker/failure_ratio',
			/between 0 and 1/
		],
		['probe ref', { probe_endpoint: 'upload' }, '/defaults/health_probe/endpoint', /upload@1/],
		[
			'hmac needs a header',
			{ webhook_scheme: 'hmac_sha256' },
			'/webhook_verification/header',
			/Name the header/
		],
		[
			'ip allowlist needs entries',
			{ webhook_scheme: 'ip_allowlist' },
			'/webhook_verification/ip_allowlist',
			/at least one address/
		]
	])('%s', (_name, over, pointer, message) => {
		expect(problems(filled(over), 'create')[pointer]).toMatch(message);
	});

	it('a complete form has none; the id is not checked on edit', () => {
		expect(problems(filled(), 'create')).toEqual({});
		expect(problems(filled({ vendor_id: '' }), 'edit')).toEqual({});
	});
});

describe('vendor bodies', () => {
	it('create sends only what is set, with null webhook verification and docs url', () => {
		const r = createBody(filled({ per_account_rps: '25' }), 'Onboard Acme');
		expect(r).toEqual({
			ok: true,
			body: {
				vendor_id: 'acme',
				display_name: 'Acme',
				auth_scheme: 'static_headers',
				regions: { in: 'https://in.api.acme.example' },
				docs_url: null,
				webhook_verification: null,
				defaults: { rate_limit: { per_account_rps: 25 } },
				reason: 'Onboard Acme'
			}
		});
	});

	it('round-trips the contract example: an untouched edit changes nothing', () => {
		const before = valuesFromVendor(vendor);
		expect(before.regions).toEqual([
			{ code: 'in', url: 'https://in1.api.clevertap.com' },
			{ code: 'us', url: 'https://us1.api.clevertap.com' }
		]);
		expect(before.breaker.window_ms).toBe('30000');
		expect(hasChanges(before, before)).toBe(false);
		const r = updateBody(before, before, 'nothing');
		expect(r.ok).toBe(false);
	});

	it('update (PUT) sends every mutable field, not only the changed one', () => {
		const before = valuesFromVendor(vendor);
		const after = { ...structuredClone(before), display_name: 'CleverTap EU' };
		const r = updateBody(after, before, 'Rename for the EU rollout');
		expect(r.ok).toBe(true);
		if (!r.ok) return;
		expect(r.body).toMatchObject({
			display_name: 'CleverTap EU',
			auth_scheme: 'static_headers',
			regions: vendor.regions,
			defaults: vendor.defaults,
			webhook_verification: vendor.webhook_verification,
			docs_url: vendor.docs_url,
			reason: 'Rename for the EU rollout'
		});
		expect('vendor_id' in r.body).toBe(false);
	});

	it('reads repeated region inputs and checkboxes from the posted form', () => {
		const form = new FormData();
		form.set('vendor_id', 'acme');
		form.set('display_name', 'Acme');
		form.set('auth_scheme', 'bearer');
		form.append(NAMES.regionCode, 'in');
		form.append(NAMES.regionUrl, 'https://in.example');
		form.append(NAMES.regionCode, 'us');
		form.append(NAMES.regionUrl, 'https://us.example');
		form.set(NAMES.breaker('min_calls'), '20');
		const v = valuesFromForm(form);
		expect(v.regions).toEqual([
			{ code: 'in', url: 'https://in.example' },
			{ code: 'us', url: 'https://us.example' }
		]);
		expect(v.breaker.min_calls).toBe('20');
		expect(v.probe_dry).toBe(false);
	});

	it('summarises breaker defaults', () => {
		expect(breakerText(undefined)).toBe('Hub defaults');
		expect(breakerText({ window_ms: 30000 })).toBe('Window (ms) 30,000; the rest Hub defaults');
	});
});

describe('endpoint form', () => {
	it('checks name, path and numbers', () => {
		expect(endpointProblems(emptyEndpoint())).toMatchObject({
			'/name': expect.stringMatching(/Enter an endpoint name/),
			'/path': expect.stringMatching(/Enter the path/)
		});
		expect(
			endpointProblems({ ...emptyEndpoint(), name: 'Upload', path: 'x', timeout_ms: '0' })
		).toMatchObject({
			'/name': expect.stringMatching(/Lower-case/),
			'/path': expect.stringMatching(/Start the path/),
			'/timeout_ms': expect.stringMatching(/at least 1/)
		});
	});

	it('a new version starts from the latest one and keeps its name', () => {
		const e = contractMocks.getVendorEndpoint.example;
		const v = valuesFromEndpoint(e);
		const r = createEndpointBody({ ...v, path: '/2/upload' }, 'Upload v2 path');
		expect(r).toEqual({
			ok: true,
			body: {
				name: 'upload',
				method: 'POST',
				path: '/2/upload',
				timeout_ms: 3000,
				connect_timeout_ms: 500,
				idempotent: true,
				stream: false,
				batch_max: 1000,
				request_schema: e.request_schema,
				reason: 'Upload v2 path'
			}
		});
	});

	it('checks each request part as a JSON Schema and the path parameters against the path', () => {
		const base = { ...emptyEndpoint(), name: 'user', method: 'GET', path: '/users/{id}' };
		expect(endpointProblems(base)['/request_schema/path']).toMatch(
			'Declare the path parameter {id} of the path.'
		);
		expect(pathParams('/users/{id}/orders/{order_id}')).toEqual(['id', 'order_id']);
		const fixed = { ...base, request_schema: { path: pathSchemaFor(base.path)! } };
		expect(fixed.request_schema.path).toEqual({
			type: 'object',
			required: ['id'],
			properties: { id: { type: 'string', minLength: 1 } }
		});
		expect(endpointProblems(fixed)).toEqual({});
		expect(endpointProblems({ ...fixed, path: '/users' })['/request_schema/path']).toMatch(
			'id is not in the path (/users).'
		);
		expect(
			endpointProblems({
				...fixed,
				request_schema: { ...fixed.request_schema, query: { type: 'obj' } }
			})['/request_schema/query']
		).toMatch(/"type" must be one of/);
		expect(
			endpointProblems({ ...fixed, response_schema: { required: 'x' } })['/response_schema']
		).toMatch(/"required" must be a list/);
	});

	it('sends only the request parts that are there, and the response schema when given', () => {
		const query = { type: 'object', required: ['name'], properties: { name: { type: 'string' } } };
		const r = createEndpointBody(
			{
				...emptyEndpoint(),
				name: 'estimate',
				method: 'GET',
				path: '/',
				idempotent: true,
				request_schema: { query, body: {} },
				response_schema: { type: 'object', properties: { age: { type: ['integer', 'null'] } } }
			},
			'Age estimate endpoint'
		);
		expect(r.ok && r.body.request_schema).toEqual({ query });
		expect(r.ok && r.body.response_schema).toMatchObject({ type: 'object' });
		const none = createEndpointBody(
			{ ...emptyEndpoint(), name: 'ping', method: 'GET', path: '/' },
			'Ping endpoint'
		);
		expect(none.ok && none.body.request_schema).toEqual({});
		expect(none.ok && 'response_schema' in none.body).toBe(false);
	});

	it('reads the request and response schemas posted as JSON fields', () => {
		const form = new FormData();
		form.set('name', 'estimate');
		form.set('request_schema', '{"query": {"type": "object"}}');
		form.set('response_schema', 'not json');
		const v = endpointValuesFromForm(form);
		expect(v.request_schema).toEqual({ query: { type: 'object' } });
		expect(v.response_schema).toBeNull();
	});

	it('knows the latest version per name and sorts newest first', () => {
		const e = contractMocks.getVendorEndpoint.example;
		const list = [
			e,
			{ ...e, endpoint_ref: 'upload@2', version: 2 },
			{ ...e, name: 'a', endpoint_ref: 'a@1' }
		];
		expect(latestByName(list).get('upload')?.version).toBe(2);
		expect(sortEndpoints(list).map((x) => x.endpoint_ref)).toEqual(['a@1', 'upload@2', 'upload@1']);
	});
});

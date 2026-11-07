import { describe, expect, it } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import {
	buildFixtures,
	createBody,
	draftKey,
	emptyValues,
	fieldsFor,
	problems,
	rowsFromJson,
	valuesFromVersion,
	type FormValues
} from './authoring';

const latest = contractMocks.getTransformer.example.latest;

const filled = (over: Partial<FormValues> = {}): FormValues => ({
	...emptyValues(),
	transformer_id: 'T9',
	vendor_endpoint: 'clevertap.upload@1',
	yaml: 'transformer: T9\nkind: request\n',
	fixtures: [
		{
			name: 'order_placed',
			outcome: 'response',
			json: { input: '{"user": {"id": "u_1"}}', expected_request: '{"method": "POST"}' }
		}
	],
	...over
});

describe('transformer authoring form', () => {
	it('starts the next version from the latest one', () => {
		const v = valuesFromVersion(latest);
		expect(v).toMatchObject({
			transformer_id: 'T1',
			kind: 'request',
			vendor_endpoint: 'clevertap.upload@1',
			yaml: latest.yaml
		});
		expect(v.fixtures[0]!.name).toBe('order_placed');
		expect(JSON.parse(v.fixtures[0]!.json.input!)).toEqual(latest.fixtures[0]!.input);
		if (latest.fixtures[0]!.input_headers)
			expect(JSON.parse(v.fixtures[0]!.json.input_headers!)).toEqual(
				latest.fixtures[0]!.input_headers
			);
		expect(problems(v, 'version')).toEqual({});
	});

	it.each<[string, Partial<FormValues>, string, RegExp]>([
		['id', { transformer_id: '9T' }, '/transformer_id', /starting with a letter/],
		['endpoint', { vendor_endpoint: 'upload@1' }, '/vendor_endpoint', /clevertap.upload@1/],
		['source', { yaml: ' ' }, '/yaml', /Write the transformer source/],
		['docs', { docs_url: 'ftp://x' }, '/docs_url', /http\(s\)/],
		['no fixtures', { fixtures: [] }, '/fixtures', /at least one/]
	])('%s', (_n, over, ptr, msg) => {
		expect(problems(filled(over), 'new')[ptr]).toMatch(msg);
	});

	it('a response transformer needs no vendor endpoint and sends none', () => {
		const v = filled({
			kind: 'response',
			vendor_endpoint: '',
			fixtures: [
				{
					name: 'known_name',
					outcome: 'response',
					json: {
						vendor_response: '{"status": 200, "body": {"age": 57}}',
						expected_response: '{"age": 57}'
					}
				}
			]
		});
		expect(problems(v, 'new')).toEqual({});
		const r = createBody(v, 'new', null, 'Return only the age');
		expect(r.ok && 'vendor_endpoint' in r.body).toBe(false);
		expect(problems(filled({ vendor_endpoint: '' }), 'new')['/vendor_endpoint']).toMatch(
			/clevertap.upload@1/
		);
	});

	it('request samples carry optional caller headers as strings', () => {
		const ok = buildFixtures(
			[
				{
					name: 'with_source',
					outcome: 'response',
					json: {
						input: '{"firstName": "Mich"}',
						input_headers: '{"X-Request-Source": "checkout"}',
						expected_request: '{"method": "GET"}'
					}
				}
			],
			'request'
		);
		expect(ok.ok && ok.fixtures[0]!.input_headers).toEqual({ 'X-Request-Source': 'checkout' });
		const bad = buildFixtures(
			[
				{
					name: 'bad',
					outcome: 'response',
					json: { input: '{}', input_headers: '{"X-N": 1}', expected_request: '{}' }
				}
			],
			'request'
		);
		expect(!bad.ok && bad.fields['/fixtures/0/input_headers']).toMatch(/must be a string/);
	});

	it('checks each fixture field as a JSON object, by pointer', () => {
		const r = buildFixtures(
			[
				{ name: 'Bad Name', outcome: 'response', json: { input: '{', expected_request: '[]' } },
				{ name: 'ok', outcome: 'response', json: { input: '{}', expected_request: '{}' } },
				{ name: 'ok', outcome: 'response', json: { input: '{}', expected_request: '{}' } }
			],
			'request'
		);
		expect(r.ok).toBe(false);
		if (r.ok) return;
		expect(r.fields['/fixtures/0/name']).toMatch(/Lower-case/);
		expect(r.fields['/fixtures/0/input']).toMatch(/not valid JSON/);
		expect(r.fields['/fixtures/0/expected_request']).toMatch(/JSON object/);
		expect(r.fields['/fixtures/2/name']).toMatch(/used twice/);
	});

	it('response fixtures expect a canonical response or a canonical error', () => {
		expect(fieldsFor('response', 'response')).toEqual([
			'vendor_response',
			'expected_response',
			'account_config'
		]);
		expect(fieldsFor('response', 'error')).toEqual([
			'vendor_response',
			'expected_error',
			'account_config'
		]);
		const r = buildFixtures(
			[
				{
					name: 'rate_limited',
					outcome: 'error',
					json: {
						vendor_response: '{"status": 429}',
						expected_error: '{"code": "UPSTREAM_RATE_LIMITED"}',
						expected_response: '{"ignored": true}'
					}
				}
			],
			'response'
		);
		expect(r).toEqual({
			ok: true,
			fixtures: [
				{
					name: 'rate_limited',
					vendor_response: { status: 429 },
					expected_error: { code: 'UPSTREAM_RATE_LIMITED' }
				}
			]
		});
	});

	it('the draft key changes with the source or a fixture, not with the reason or id', () => {
		const a = filled();
		expect(draftKey({ ...a, transformer_id: 'T10' })).toBe(draftKey(a));
		expect(draftKey({ ...a, yaml: `${a.yaml}# x\n` })).not.toBe(draftKey(a));
		const b = structuredClone(a);
		b.fixtures[0]!.json.expected_request = '{"method": "PUT"}';
		expect(draftKey(b)).not.toBe(draftKey(a));
	});

	it('builds TransformerVersionCreate (new id: no expected version; next version: expected)', () => {
		expect(createBody(filled(), 'new', null, 'First mapping')).toEqual({
			ok: true,
			body: {
				kind: 'request',
				vendor_endpoint: 'clevertap.upload@1',
				yaml: 'transformer: T9\nkind: request\n',
				expected_latest_version: null,
				fixtures: [
					{
						name: 'order_placed',
						input: { user: { id: 'u_1' } },
						expected_request: { method: 'POST' }
					}
				],
				docs_url: null,
				reason: 'First mapping'
			}
		});
		const r = createBody(filled(), 'version', 7, 'Map context.campaign');
		expect(r.ok && r.body.expected_latest_version).toBe(7);
	});

	it('reads the posted fixture rows and drops unknown fields', () => {
		expect(rowsFromJson('nope')).toBeNull();
		expect(rowsFromJson('[{"name":"a","outcome":"x","json":{"input":"{}","evil":"1"}}]')).toEqual([
			{ name: 'a', outcome: 'response', json: { input: '{}' } }
		]);
	});
});

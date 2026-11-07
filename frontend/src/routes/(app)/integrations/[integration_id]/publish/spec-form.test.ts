import { describe, expect, it } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import type { IntegrationSpec } from '$lib/api/types';
import { AADHAAR } from '$lib/testing/aadhaar';
import { parseSpecYaml, specToYaml } from '$lib/utils/spec-yaml';
import {
	emptyTarget,
	fieldPointers,
	formProblems,
	forForm,
	parseSchedule,
	problemsByField,
	setOwnPair,
	starterSpec,
	transformerOptions,
	unknownOperations,
	weightsTotal,
	yamlOnlySections
} from './spec-form';

const OPS = ['send_otp', 'verify_otp'];

describe('the form model', () => {
	it('represents int_aadhaar_okyc and renders it back to the same spec', () => {
		const spec = forForm(structuredClone(AADHAAR) as never);
		expect(formProblems(spec, OPS)).toEqual([]);
		expect(spec.targets[0]!.operations).toEqual({
			verify_otp: { request: 'CF-OKYC-VERIFY-REQ@1', response: 'CF-OKYC-VERIFY-RES@1' }
		});
		const back = parseSpecYaml(specToYaml(spec));
		expect(back.ok && back.value).toEqual(AADHAAR);
	});

	it('gives an operation its own pair from the default one, and drops it back', () => {
		const t = emptyTarget(100);
		t.transformer = { request: 'A@1', response: 'B@1' };
		setOwnPair(t, 'verify_otp', true);
		expect(t.operations).toEqual({ verify_otp: { request: 'A@1', response: 'B@1' } });
		setOwnPair(t, 'verify_otp', false);
		expect('operations' in t).toBe(false);
	});

	it('fills the sections the form shows and keeps the ones it does not', () => {
		const active = contractMocks.getIntegration.example.active!.spec;
		const spec = forForm(active as never);
		expect(spec).toMatchObject(active);
		expect(forForm({})).toEqual({
			targets: [],
			delivery: { mode: 'wait' },
			audit: { payloads: 'masked' }
		});
		expect(yamlOnlySections({ ...spec, failover: [spec.targets[0]!], shadow: null })).toContain(
			'failover (1)'
		);
	});

	it('starts interactive capabilities in sync mode', () => {
		expect(starterSpec(true).delivery.mode).toBe('sync');
		expect(starterSpec(false).delivery).toMatchObject({ mode: 'wait', wait_ms: 3000 });
	});

	it('parses a retry schedule', () => {
		expect(parseSchedule('1000, 5000,30000')).toEqual([1000, 5000, 30000]);
		expect(parseSchedule('')).toEqual([]);
		expect(parseSchedule('1s')).toBeNull();
	});
});

describe('console checks', () => {
	it.each<[string, (s: IntegrationSpec) => void, string, RegExp]>([
		['no target', (s) => (s.targets = []), '/targets', /at least one target/],
		['vendor', (s) => (s.targets[0]!.vendor_id = ''), '/targets/0/vendor_id', /Pick the vendor/],
		['weight', (s) => (s.targets[0]!.weight = 101), '/targets/0/weight', /0 to 100/],
		[
			'default pair',
			(s) => (s.targets[0]!.transformer.request = 'T1'),
			'/targets/0/transformer/request',
			/such as T1@7/
		],
		[
			'operation pair',
			(s) => (s.targets[0]!.operations!.verify_otp!.response = ''),
			'/targets/0/operations/verify_otp/response',
			/Pick the response transformer/
		],
		[
			'unknown operation',
			(s) => (s.targets[0]!.operations!.bogus = { request: 'A@1', response: 'B@1' }),
			'/targets/0/operations/bogus',
			/no operation "bogus"/
		],
		[
			'weights total',
			(s) => s.targets.push({ ...structuredClone(s.targets[0]!), weight: 20 }),
			'/targets',
			/total 120/
		]
	])('%s', (_n, change, pointer, message) => {
		const spec = structuredClone(AADHAAR);
		change(spec);
		const found = formProblems(spec, OPS).find((p) => p.pointer === pointer);
		expect(found?.message).toMatch(message);
	});

	it('knows unknown operations and the weights total', () => {
		const t = structuredClone(AADHAAR.targets[0]!);
		t.operations!.bogus = { request: 'A@1', response: 'B@1' };
		expect(unknownOperations(t, OPS)).toEqual(['bogus']);
		expect(weightsTotal(AADHAAR)).toBe(100);
	});
});

describe('problems on fields', () => {
	it('puts each pointer on the deepest field that shows it; the rest stay unclaimed', () => {
		const pointers = fieldPointers(AADHAAR, OPS);
		expect(pointers).toContain('/targets/0/operations/verify_otp/request');
		expect(pointers).toContain('/targets/0/operations/send_otp');
		const { byField, unclaimed } = problemsByField(
			[
				{ pointer: '/targets/0/operations/verify_otp', message: 'compiled for send_otp' },
				{ pointer: '/targets/0/account_id', message: 'unknown account' },
				{ pointer: '/delivery', message: 'mode wait needs polling' },
				{ pointer: '/failover/0/account_id', message: 'retired' }
			],
			pointers
		);
		expect(byField.get('/targets/0/operations/verify_otp')).toEqual(['compiled for send_otp']);
		expect(byField.get('/targets/0/account_id')).toEqual(['unknown account']);
		expect(byField.get('/delivery')).toEqual(['mode wait needs polling']);
		expect(unclaimed.map((p) => p.pointer)).toEqual(['/failover/0/account_id']);
	});
});

describe('transformer options', () => {
	const list = contractMocks.listTransformers.example.items;
	it('offers the vendor transformers of the wanted kind, newest version first', () => {
		const req = transformerOptions(list, 'request', 'clevertap');
		expect(req[0]).toMatch(/^T\w*@\d+$/);
		expect(
			req.every((r) =>
				list.some((t) => r.startsWith(`${t.transformer_id}@`) && t.kind === 'request')
			)
		).toBe(true);
		expect(transformerOptions(list, 'request', 'someone-else')).toEqual([]);
	});
});

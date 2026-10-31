// @vitest-environment node
import { Ajv2020 } from 'ajv/dist/2020.js';
import addFormats from 'ajv-formats';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import YAML from 'yaml';
import { adminClient } from '../client';
import { contractMocks } from './contract.gen';
import { createHandlers } from './handlers';
import { presets, scenarioFromCookieString, scenarioHandlers, SCENARIO_COOKIE } from './scenarios';
import { mockAdminFetch } from './server-fetch';
import { server } from './server';

// HUB-259: mock scenarios (mock builds and e2e) stay inside the contract.

const spec = YAML.parse(
	readFileSync(resolve(process.cwd(), '../contracts/openapi/hub-admin.v1.yaml'), 'utf8')
);
const ajv = new Ajv2020({ strict: false, allErrors: true });
addFormats(ajv);
ajv.addSchema({ ...spec, $id: 'https://hub.internal/contracts/hub-admin.v1.yaml' });
const schema = (name: string) =>
	ajv.compile({
		$ref: `https://hub.internal/contracts/hub-admin.v1.yaml#/components/schemas/${name}`
	});

const api = () => adminClient({ baseUrl: 'http://hub.test/admin/v1' });

describe('presets', () => {
	it.each(Object.keys(presets))(
		'%s answers bodies that validate against the contract',
		async (name) => {
			server.use(...presets[name]!());
			const checks: [string, () => Promise<unknown>][] = [
				[
					'Integration',
					async () =>
						(
							await api().raw.GET('/integrations/{integration_id}', {
								params: { path: { integration_id: 'int_evt_brandA' } }
							})
						).data
				],
				[
					'IntegrationVersionList',
					async () =>
						(
							await api().raw.GET('/integrations/{integration_id}/versions', {
								params: { path: { integration_id: 'int_evt_brandA' } }
							})
						).data
				],
				[
					'IntegrationVersion',
					async () =>
						(
							await api().raw.GET('/integrations/{integration_id}/versions/{version}', {
								params: { path: { integration_id: 'int_evt_brandA', version: 8 } }
							})
						).data
				],
				['BreakerList', async () => (await api().raw.GET('/breakers')).data]
			];
			for (const [schemaName, get] of checks) {
				const v = schema(schemaName);
				const body = await get();
				expect(v(body), `${name} ${schemaName}: ${JSON.stringify(v.errors)}`).toBe(true);
			}
		}
	);

	it('v8-active makes v8 the active version with the draft example spec', async () => {
		server.use(...presets['v8-active']!());
		const { data } = await api().raw.GET('/integrations/{integration_id}', {
			params: { path: { integration_id: 'int_evt_brandA' } }
		});
		expect(data).toMatchObject({ active_version: 8, has_draft: false });
		expect(data?.active?.spec.targets).toHaveLength(2);
	});
});

describe('default world', () => {
	it('v8 of the integration example is the draft example; other versions the v7 example', async () => {
		const get = (version: number) =>
			api().raw.GET('/integrations/{integration_id}/versions/{version}', {
				params: { path: { integration_id: 'int_evt_brandA', version } }
			});
		expect((await get(8)).data).toEqual(
			contractMocks.createIntegrationDraft.example.integration_version
		);
		expect((await get(7)).data).toEqual(contractMocks.getIntegrationVersion.example);
	});
});

describe('scenarioHandlers', () => {
	it.each([
		[null, 0],
		['', 0],
		['nonsense', 0],
		['publishIntegrationVersion:409', 1],
		// 412 is not declared for publish: ignored rather than inventing an error.
		['publishIntegrationVersion:412', 0],
		['notAnOperation:409', 0],
		['v8-active+rollbackIntegration:409', 4]
	])('%j -> %i handlers', (value, n) => expect(scenarioHandlers(value)).toHaveLength(n));

	it('an error token answers the contract error example', async () => {
		server.use(...scenarioHandlers('rollbackIntegration:409'));
		const res = await api().call((c) =>
			c.POST('/integrations/{integration_id}/rollback', {
				params: { path: { integration_id: 'int_evt_brandA' } },
				body: { to_version: 7, expected_active_version: 8, reason: 'x'.repeat(10) }
			})
		);
		expect(!res.ok && res.error.code).toBe('CONFLICT');
	});

	it('reads the cookie from document.cookie', () => {
		expect(scenarioFromCookieString(`a=1; ${SCENARIO_COOKIE}=v8-active%2Bx; b=2`)).toBe(
			'v8-active+x'
		);
		expect(scenarioFromCookieString('a=1')).toBeNull();
	});
});

describe('mockAdminFetch (server side, mock builds)', () => {
	const event = (cookie?: string) => ({
		url: new URL('http://console.test/integrations/int_evt_brandA'),
		cookies: { get: (k: string) => (k === SCENARIO_COOKIE ? cookie : undefined) }
	});

	it('ignores anything that is not the admin API', async () => {
		expect(
			await mockAdminFetch(event() as never, new Request('http://console.test/login'))
		).toBeUndefined();
		expect(
			await mockAdminFetch(event() as never, new Request('http://other.test/admin/v1/me'))
		).toBeUndefined();
	});

	it('answers admin calls with the contract examples and honours the scenario cookie', async () => {
		const ok = await mockAdminFetch(
			event() as never,
			new Request('http://console.test/admin/v1/me')
		);
		expect(await ok?.json()).toEqual(contractMocks.getMe.example);
		const res = await mockAdminFetch(
			event('pauseIntegration:403') as never,
			new Request('http://console.test/admin/v1/integrations/int_evt_brandA/pause', {
				method: 'POST',
				body: '{"reason":"x"}'
			})
		);
		expect(res?.status).toBe(403);
		expect(createHandlers().length).toBe(79);
	});
});

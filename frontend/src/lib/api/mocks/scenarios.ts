import type { HttpHandler } from 'msw';
import type { OperationResponse } from '../types';
import { contractMocks } from './contract.gen';
import { mockError, mockOperation } from './handlers';
import type { OperationId } from './types';

/**
 * Mock scenarios (mock builds only: `npm run dev:mock`, `build:mock`, e2e). The default mock world
 * is the contract examples as they are. A scenario swaps a few answers for other states that the
 * examples also describe, so flows such as rollback or resume can be exercised without a backend.
 *
 * Select one with the cookie `hub_mock_scenario` (tokens joined by `+`), e.g. `v8-active` or
 * `v8-active+rollbackIntegration:409`. Tokens:
 * - a preset name (below), built only from contract example values;
 * - `<operationId>:<status>`: that operation answers its contract error example for `status`
 *   (only statuses the contract declares for the operation).
 * Both the browser worker and the server (form actions, `hooks.server.ts`) honour the cookie.
 */
export const SCENARIO_COOKIE = 'hub_mock_scenario';

const integration = contractMocks.getIntegration.example;
const versions = contractMocks.listIntegrationVersions.example;
const draft = contractMocks.createIntegrationDraft.example.integration_version;
const published = contractMocks.publishIntegrationVersion.example;

type Body<Op extends OperationId> = OperationResponse<Op>;

/** v8 (the draft example) after the publish example: v8 active with two targets, v7 published. */
const v8: Body<'getIntegrationVersion'> = {
	...draft,
	state: 'published',
	published_at: published.published_at ?? null,
	published_by: draft.author,
	snapshot_version: published.snapshot_version ?? null
};

export const presets: Record<string, () => HttpHandler[]> = {
	/** After publishing the draft: rollback (to v7) and weights (two targets) have something to do. */
	'v8-active': () => [
		mockOperation('getIntegration', {
			...integration,
			active_version: 8,
			latest_version: 8,
			has_draft: false,
			active: v8
		}),
		mockOperation('listIntegrationVersions', {
			...versions,
			items: versions.items.map((v) =>
				v.version === 8
					? { ...v, state: 'published' as const, published_at: v8.published_at, is_active: true }
					: { ...v, is_active: false }
			)
		}),
		mockOperation('getIntegrationVersion', (params) =>
			params.version === '8' ? v8 : contractMocks.getIntegrationVersion.example
		)
	],
	/** After the pause example: the integration is paused with that example's reason. */
	paused: () => [
		mockOperation('getIntegration', {
			...integration,
			paused: true,
			paused_reason: contractMocks.pauseIntegration.example.integration.paused_reason
		})
	],
	/** After the force-open example: the board's breaker is held open by an operator. */
	'breaker-forced': () => [
		mockOperation('listBreakers', {
			items: [contractMocks.forceOpenBreaker.example.breaker],
			next_cursor: null
		})
	]
};

/** Handlers for a scenario cookie value (unknown tokens are ignored). Put them before the defaults. */
export function scenarioHandlers(value: string | null | undefined): HttpHandler[] {
	if (!value) return [];
	const out: HttpHandler[] = [];
	for (const token of value.split('+')) {
		const preset = presets[token];
		if (preset) {
			out.push(...preset());
			continue;
		}
		const m = /^([A-Za-z]+):(\d{3})$/.exec(token);
		if (!m) continue;
		const op = m[1] as OperationId;
		if (!(op in contractMocks) || !contractMocks[op].errors[m[2]!]) continue;
		out.push(mockError(op, Number(m[2])));
	}
	return out;
}

/** Reads the scenario cookie from a `document.cookie` string. */
export function scenarioFromCookieString(cookies: string): string | null {
	for (const part of cookies.split(';')) {
		const [k, ...v] = part.trim().split('=');
		if (k === SCENARIO_COOKIE) return decodeURIComponent(v.join('='));
	}
	return null;
}

import type { HttpHandler } from 'msw';
import type {
	Account,
	Integration,
	IntegrationVersion,
	OperationResponse,
	TransformerVersion,
	Vendor,
	VendorEndpoint,
	VendorSummary
} from '../types';
import { contractMocks } from './contract.gen';
import { mockEcho, mockOperation } from './handlers';

/**
 * Mock world of onboarding a public API from the panel (HUB-436, ADR-0023), from the contract's
 * named examples for the age estimate: vendor `agify` (auth `none`), endpoint `estimate@1`
 * (`GET /`, required query `name`), account `agify-public` (no credentials), request transformer
 * `AGE_REQ` and response transformer `AGE_RES` with samples, integration `int_age_estimate`
 * (body `{firstName, lastName}`, pass-through draft). The mocks stay stateless: create calls echo
 * what was sent, and reads of these ids answer as if everything existed; other ids keep the
 * default examples. Selected with the scenario `onboarding` (see scenarios.ts).
 */

const named = <T>(v: T | undefined, what: string): T => {
	if (v === undefined) throw new Error(`contract example missing: ${what}`);
	return v;
};

const vendorReq = named(contractMocks.createVendor.named?.request?.public_api, 'public_api');
const endpointReq = named(
	contractMocks.createVendorEndpoint.named?.request?.query_only,
	'query_only'
);
const accountReq = named(contractMocks.createAccount.named?.request?.auth_none, 'auth_none');
const reqTransformer = named(
	contractMocks.createTransformerVersion.named?.request?.request_query,
	'request_query'
);
const resTransformer = named(
	contractMocks.createTransformerVersion.named?.request?.response,
	'response'
);
const passThrough = named(
	contractMocks.createIntegrationDraft.named?.request?.pass_through,
	'pass_through'
);

const AT = contractMocks.createVendor.example.vendor.created_at;
const AUTHOR = contractMocks.createTransformerVersion.example.transformer_version.author;
export const ONBOARDING = {
	vendor: vendorReq.vendor_id,
	account: accountReq.account_id,
	integration: 'int_age_estimate',
	request: 'AGE_REQ',
	response: 'AGE_RES'
} as const;

function endpointOf(body: typeof endpointReq, version = 1): VendorEndpoint {
	const { reason: _reason, ...fields } = body;
	return {
		...fields,
		endpoint_ref: `${body.name}@${version}`,
		version,
		timeout_ms: body.timeout_ms ?? 3000,
		connect_timeout_ms: body.connect_timeout_ms ?? 500,
		response_schema: body.response_schema ?? null,
		created_at: AT
	};
}

function vendorOf(body: typeof vendorReq, endpoints: VendorEndpoint[]): Vendor {
	const { reason: _reason, ...fields } = body;
	return { ...fields, endpoints, revision: 1, created_at: AT, updated_at: AT };
}

const agify = vendorOf(vendorReq, [endpointOf(endpointReq)]);
const agifySummary: VendorSummary = {
	vendor_id: vendorReq.vendor_id,
	display_name: vendorReq.display_name ?? vendorReq.vendor_id,
	auth_scheme: vendorReq.auth_scheme ?? 'none',
	regions: Object.keys(vendorReq.regions ?? {}),
	endpoint_count: agify.endpoints.length,
	account_count: 1,
	revision: 1,
	updated_at: AT
};

function accountOf(body: typeof accountReq): Account {
	return {
		account_id: body.account_id,
		vendor_id: body.vendor_id,
		region: body.region,
		state: 'active',
		config: body.config,
		quota: body.quota ?? {},
		credentials: [],
		integrations_using: [],
		pinned_requests: 0,
		open_sessions: 0,
		dead_letter_count: 0,
		dead_letter_oldest_at: null,
		created_at: AT,
		updated_at: AT
	};
}

function transformerOf(id: string, body: typeof reqTransformer, version = 1): TransformerVersion {
	return {
		transformer_id: id,
		version,
		ref: `${id}@${version}`,
		kind: body.kind,
		vendor_endpoint: body.vendor_endpoint ?? null,
		yaml: body.yaml,
		fixtures: body.fixtures,
		docs_url: body.docs_url ?? null,
		goldens: { passed: true, total: body.fixtures.length, failed: 0 },
		author: AUTHOR,
		reason: body.reason,
		created_at: AT
	};
}

const known: Record<string, TransformerVersion> = {
	[ONBOARDING.request]: transformerOf(ONBOARDING.request, reqTransformer),
	[ONBOARDING.response]: transformerOf(ONBOARDING.response, resTransformer)
};

const integration: Integration = {
	...contractMocks.getIntegration.example,
	integration_id: ONBOARDING.integration,
	description: passThrough.spec?.operations.default?.description ?? null,
	grants: [],
	interactive: false,
	moves_money_or_quota: false,
	active_version: null,
	latest_version: null,
	has_draft: false,
	paused: false,
	paused_reason: null,
	active: null
};

function draftOf(id: string, version: number, body: typeof passThrough): IntegrationVersion {
	return {
		integration_id: id,
		version,
		state: 'draft',
		spec: body.spec ?? passThrough.spec!,
		based_on: body.based_on ?? null,
		author: AUTHOR,
		reason: body.reason,
		created_at: AT,
		published_at: null,
		published_by: null,
		snapshot_version: null
	};
}

type Body<Op extends keyof typeof contractMocks> = OperationResponse<Op>;

export function onboardingHandlers(): HttpHandler[] {
	const ex = contractMocks;
	return [
		// Vendor and endpoint.
		mockEcho('createVendor', (_p, b): Body<'createVendor'> => ({
			change_id: ex.createVendor.example.change_id,
			vendor: vendorOf({ ...vendorReq, ...b }, [])
		})),
		mockOperation('getVendor', (p) =>
			p.vendor_id === agify.vendor_id ? agify : ex.getVendor.example
		),
		mockOperation('listVendors', {
			...ex.listVendors.example,
			items: [...ex.listVendors.example.items, agifySummary]
		}),
		mockEcho('createVendorEndpoint', (p, b): Body<'createVendorEndpoint'> | undefined =>
			p.vendor_id === agify.vendor_id
				? { change_id: ex.createVendorEndpoint.example.change_id, endpoint: endpointOf(b) }
				: undefined
		),
		mockOperation('getVendorEndpoint', (p) =>
			p.vendor_id === agify.vendor_id ? agify.endpoints[0]! : ex.getVendorEndpoint.example
		),
		// Account without credentials.
		mockEcho('createAccount', (_p, b): Body<'createAccount'> | undefined =>
			b.vendor_id === agify.vendor_id
				? {
						change_id: ex.createAccount.example.change_id,
						account: accountOf({ ...accountReq, ...b })
					}
				: undefined
		),
		mockOperation('getAccount', (p) =>
			p.account_id === ONBOARDING.account ? accountOf(accountReq) : ex.getAccount.example
		),
		// Transformers proven by their samples.
		mockEcho('runTransformerGoldens', (_p, b): Body<'runTransformerGoldens'> | undefined =>
			b.fixtures
				? {
						passed: true,
						total: b.fixtures.length,
						failed: 0,
						results: b.fixtures.map((f) => ({
							fixture: f.name,
							passed: true,
							diff: [],
							schema_errors: []
						})),
						duration_ms: 3
					}
				: undefined
		),
		mockEcho('createTransformerVersion', (p, b): Body<'createTransformerVersion'> => ({
			change_id: ex.createTransformerVersion.example.change_id,
			transformer_version: transformerOf(p.transformer_id!, b, (b.expected_latest_version ?? 0) + 1)
		})),
		mockOperation(
			'getTransformerVersion',
			(p) => known[p.transformer_id!] ?? ex.getTransformerVersion.example
		),
		mockOperation('getTransformer', (p) => {
			const t = known[p.transformer_id!];
			if (!t) return ex.getTransformer.example;
			return {
				transformer_id: t.transformer_id,
				kind: t.kind,
				vendor_endpoint: t.vendor_endpoint,
				latest_version: t.version,
				used_by_active_versions: 0,
				updated_at: AT,
				latest: t
			};
		}),
		// Integration: created, first draft, publish.
		mockEcho('createIntegration', (_p, b): Body<'createIntegration'> => ({
			change_id: ex.createIntegration.example.change_id,
			integration: {
				...integration,
				integration_id: b.integration_id,
				owner_tenant: b.owner_tenant,
				description: b.description ?? null,
				grants: b.grants ?? [],
				interactive: b.interactive ?? false,
				moves_money_or_quota: b.moves_money_or_quota ?? false
			}
		})),
		mockOperation('getIntegration', (p) =>
			p.integration_id === ONBOARDING.integration ? integration : ex.getIntegration.example
		),
		mockEcho('createIntegrationDraft', (p, b): Body<'createIntegrationDraft'> | undefined =>
			p.integration_id === ONBOARDING.integration
				? {
						change_id: ex.createIntegrationDraft.example.change_id,
						integration_version: draftOf(p.integration_id, 1, { ...passThrough, ...b })
					}
				: undefined
		),
		mockEcho('updateIntegrationDraft', (p, b): Body<'updateIntegrationDraft'> | undefined =>
			p.integration_id === ONBOARDING.integration
				? {
						change_id: ex.updateIntegrationDraft.example.change_id,
						integration_version: draftOf(p.integration_id, Number(p.version), {
							...passThrough,
							...b
						})
					}
				: undefined
		),
		mockOperation('getIntegrationVersion', (p) =>
			p.integration_id === ONBOARDING.integration
				? draftOf(p.integration_id, Number(p.version), passThrough)
				: ex.getIntegrationVersion.example
		),
		mockOperation('publishIntegrationVersion', (p) =>
			p.integration_id === ONBOARDING.integration
				? {
						...ex.publishIntegrationVersion.example,
						integration_id: p.integration_id,
						previous_active_version: null,
						active_version: Number(p.version),
						created_version: null
					}
				: ex.publishIntegrationVersion.example
		)
	];
}

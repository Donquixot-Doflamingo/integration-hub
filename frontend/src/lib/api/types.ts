import type { components, operations } from './schema';

/**
 * Friendly aliases for generated contract types. This is the ONLY place outside `schema.d.ts`
 * where API type names are declared (enforced by `scripts/check-api-types.mjs`).
 * Add an alias here when a screen needs one; never redeclare fields.
 */
export type Schemas = components['schemas'];
export type Operations = operations;

/** Success (2xx) JSON body of an operation, by operationId. */
export type OperationResponse<Op extends keyof operations> =
	operations[Op]['responses'] extends infer R
		? {
				[S in keyof R]: S extends 200 | 201 | 202
					? R[S] extends { content: { 'application/json': infer B } }
						? B
						: never
					: never;
			}[keyof R]
		: never;

export type Me = Schemas['Me'];
export type Role = Schemas['Role'];
export type BreakerState = Schemas['BreakerState'];
export type RequestState = Schemas['RequestState'];
export type Slug = Schemas['Slug'];

/** Identity a console session carries (subset of `Me`; no token, safe to send to the browser). */
export type SessionUser = Pick<Me, 'subject' | 'email' | 'name' | 'roles' | 'tenants'>;

export type IntegrationSummary = Schemas['IntegrationSummary'];
export type Integration = Schemas['Integration'];
export type IntegrationVersion = Schemas['IntegrationVersion'];
export type IntegrationVersionSummary = Schemas['IntegrationVersionSummary'];
export type IntegrationSpec = Schemas['IntegrationSpec'];
export type VersionDiff = Schemas['VersionDiff'];
export type DiffOp = Schemas['DiffOp'];
/** Query parameters of `listIntegrations`. */
export type ListIntegrationsQuery = NonNullable<
	Operations['listIntegrations']['parameters']['query']
>;

// Mutations (HUB-259+).
export type PublishResult = Schemas['PublishResult'];
export type ValidationReport = Schemas['ValidationReport'];
export type ValidationCheck = Schemas['ValidationCheck'];
export type IntegrationVersionChange = Schemas['IntegrationVersionChange'];
export type IntegrationChange = Schemas['IntegrationChange'];

// Health board (HUB-261, HUB-262).
export type Breaker = Schemas['Breaker'];
export type BreakerDetail = Schemas['BreakerDetail'];
export type BreakerChange = Schemas['BreakerChange'];
export type AccountSummary = Schemas['AccountSummary'];
export type AccountState = Schemas['AccountState'];

// Request explorer (HUB-263, HUB-264).
export type RequestSummary = Schemas['RequestSummary'];
export type RequestDetail = Schemas['RequestDetail'];
export type RequestTimeline = Schemas['RequestTimeline'];
export type TimelineEvent = Schemas['TimelineEvent'];
export type TimelineEventType = Schemas['TimelineEventType'];
export type Attempt = Schemas['Attempt'];
/** Query parameters of `searchRequests`. */
export type SearchRequestsQuery = NonNullable<Operations['searchRequests']['parameters']['query']>;

// DLQ console (HUB-266, HUB-267).
export type DeadLetter = Schemas['DeadLetter'];
export type DeadLetterDetail = Schemas['DeadLetterDetail'];
export type DlqFilter = Schemas['DlqFilter'];
export type DlqBulkResult = Schemas['DlqBulkResult'];
/** Query parameters of `listDeadLetters`. */
export type ListDeadLettersQuery = NonNullable<
	Operations['listDeadLetters']['parameters']['query']
>;

// Transformers (HUB-268, HUB-269).
export type TransformerKind = Schemas['TransformerKind'];
export type TransformerSummary = Schemas['TransformerSummary'];
export type Transformer = Schemas['Transformer'];
export type TransformerVersion = Schemas['TransformerVersion'];
export type TransformerVersionSummary = Schemas['TransformerVersionSummary'];
export type GoldenFixture = Schemas['GoldenFixture'];
export type GoldensRunResult = Schemas['GoldensRunResult'];
export type GoldenResult = GoldensRunResult['results'][number];
export type TransformerDryRunRequest = Schemas['TransformerDryRunRequest'];
export type TransformerDryRunResult = Schemas['TransformerDryRunResult'];
export type VendorRequestPreview = Schemas['VendorRequestPreview'];
/** Query parameters of `listTransformers`. */
export type ListTransformersQuery = NonNullable<
	Operations['listTransformers']['parameters']['query']
>;

// Tenants (HUB-270), API keys (HUB-271), registrar endpoints (HUB-272).
export type TenantSummary = Schemas['TenantSummary'];
export type Tenant = Schemas['Tenant'];
export type TenantCreate = Schemas['TenantCreate'];
export type TenantUpdate = Schemas['TenantUpdate'];
export type TenantChange = Schemas['TenantChange'];
export type RateLimit = Schemas['RateLimit'];
/** Query parameters of `listTenants`. */
export type ListTenantsQuery = NonNullable<Operations['listTenants']['parameters']['query']>;
export type ApiKey = Schemas['ApiKey'];
export type ApiKeyState = Schemas['ApiKeyState'];
export type ApiKeyScope = Schemas['ApiKeyScope'];
export type ApiKeyCreate = Schemas['ApiKeyCreate'];
export type ApiKeyChange = Schemas['ApiKeyChange'];
export type ApiKeySecretChange = Schemas['ApiKeySecretChange'];
export type ApiKeyRotateResult = Schemas['ApiKeyRotateResult'];

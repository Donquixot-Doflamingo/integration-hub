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
export type RegistrarEndpoint = Schemas['RegistrarEndpoint'];
export type RegistrarEndpointCreate = Schemas['RegistrarEndpointCreate'];
export type RegistrarEndpointUpdate = Schemas['RegistrarEndpointUpdate'];
export type RegistrarEndpointChange = Schemas['RegistrarEndpointChange'];
export type RegistrarEndpointSecretChange = Schemas['RegistrarEndpointSecretChange'];

// Change log (HUB-274) and snapshots (HUB-273).
export type ChangeLogSummary = Schemas['ChangeLogSummary'];
export type ChangeLogEntry = Schemas['ChangeLogEntry'];
export type ObjectType = Schemas['ObjectType'];
/** Query parameters of `listChangeLog`. */
export type ListChangeLogQuery = NonNullable<Operations['listChangeLog']['parameters']['query']>;
export type Snapshot = Schemas['Snapshot'];
export type SnapshotDetail = Schemas['SnapshotDetail'];

// Vendors and endpoints (HUB-290).
export type AuthScheme = Schemas['AuthScheme'];
export type VendorSummary = Schemas['VendorSummary'];
export type Vendor = Schemas['Vendor'];
export type VendorCreate = Schemas['VendorCreate'];
export type VendorUpdate = Schemas['VendorUpdate'];
export type VendorChange = Schemas['VendorChange'];
export type VendorDefaults = Schemas['VendorDefaults'];
export type BreakerConfig = Schemas['BreakerConfig'];
export type WebhookVerification = Schemas['WebhookVerification'];
export type VendorEndpoint = Schemas['VendorEndpoint'];
export type VendorEndpointCreate = Schemas['VendorEndpointCreate'];
export type VendorEndpointChange = Schemas['VendorEndpointChange'];
/** Query parameters of `listVendors`. */
export type ListVendorsQuery = NonNullable<Operations['listVendors']['parameters']['query']>;

// Accounts and credentials (HUB-291).
export type Account = Schemas['Account'];
export type AccountCreate = Schemas['AccountCreate'];
export type AccountUpdate = Schemas['AccountUpdate'];
export type AccountChange = Schemas['AccountChange'];
export type AccountQuota = Schemas['AccountQuota'];
export type CredentialSlot = Schemas['CredentialSlot'];
export type CredentialSlotsChange = Schemas['CredentialSlotsChange'];
export type ConnectionTestRequest = Schemas['ConnectionTestRequest'];
export type ConnectionTestResult = Schemas['ConnectionTestResult'];
export type LifecycleTransition = Schemas['LifecycleTransition'];
/** Query parameters of `listAccounts`. */
export type ListAccountsQuery = NonNullable<Operations['listAccounts']['parameters']['query']>;

// Integration authoring (HUB-292).
export type CapabilitySummary = Schemas['CapabilitySummary'];
export type CapabilityVersion = Schemas['CapabilityVersion'];
export type IntegrationCreate = Schemas['IntegrationCreate'];
export type TargetRef = Schemas['TargetRef'];
export type Target = Schemas['Target'];
export type TransformerPair = Schemas['TransformerPair'];
export type DeliveryPolicy = Schemas['DeliveryPolicy'];
export type DeliveryMode = Schemas['DeliveryMode'];
export type AuditPolicy = Schemas['AuditPolicy'];

// Transformer authoring (HUB-293).
export type TransformerVersionCreate = Schemas['TransformerVersionCreate'];
export type TransformerVersionChange = Schemas['TransformerVersionChange'];
export type TransformerDraft = Schemas['TransformerDraft'];
export type GoldensRunRequest = Schemas['GoldensRunRequest'];

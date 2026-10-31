export {
	adminClient,
	unwrap,
	resolveBaseUrl,
	REQUEST_ID_HEADER,
	DEFAULT_ADMIN_API_BASE,
	type AdminClient,
	type AdminClientOptions,
	type ApiResult,
	type RawAdminClient,
	type SuccessData
} from './client';
export {
	HubApiError,
	isAdminErrorEnvelope,
	type AdminError,
	type AdminErrorCode,
	type AdminErrorEnvelope,
	type HubErrorCode
} from './errors';
export type * from './types';

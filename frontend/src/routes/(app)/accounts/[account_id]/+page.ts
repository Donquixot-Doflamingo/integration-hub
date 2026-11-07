import { consoleApi } from '$lib/api/console';
import type { PageLoad } from './$types';

// Account detail (HUB-291): getAccount (viewer) with its ETag (sent back as If-Match on edits),
// and the vendor's endpoints (getVendor) for the connection test. Not loaded for tenant viewers.
export const load: PageLoad = async (event) => {
	const accountId = event.params.account_id;
	const { access } = await event.parent();
	if (access.tenantScoped)
		return {
			accountId,
			account: null,
			endpoints: Promise.resolve([]),
			authScheme: Promise.resolve(null)
		};
	const api = consoleApi(event);
	const account = api
		.call((c) => c.GET('/accounts/{account_id}', { params: { path: { account_id: accountId } } }))
		.then((r) => (r.ok ? { ...r, data: { account: r.data, etag: r.etag } } : r));
	const vendor = account.then((r) =>
		r.ok
			? api.call((c) =>
					c.GET('/vendors/{vendor_id}', {
						params: { path: { vendor_id: r.data.account.vendor_id } }
					})
				)
			: null
	);
	// The probe endpoints of the account's vendor; an empty list just hides the picker.
	const endpoints = vendor.then((v) => (v?.ok ? v.data.endpoints.map((e) => e.endpoint_ref) : []));
	// Auth scheme `none` (HUB-436): the account has no credentials to rotate.
	const authScheme = vendor.then((v) => (v?.ok ? v.data.auth_scheme : null));
	return { accountId, account, endpoints, authScheme };
};

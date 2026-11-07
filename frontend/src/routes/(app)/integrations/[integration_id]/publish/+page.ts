import type { ApiResult } from '$lib/api/client';
import { consoleApi } from '$lib/api/console';
import { collectPages } from '$lib/api/list';
import type {
	AccountSummary,
	Integration,
	IntegrationVersion,
	TransformerSummary,
	VendorSummary,
	VersionDiff
} from '$lib/api/types';
import { can } from '$lib/auth/roles';
import type { PageLoad } from './$types';

/**
 * What the structured editor offers (HUB-292): the capability's operations and whether it is
 * interactive, vendors, accounts and the capability's transformers. Each list may be empty when
 * its call fails; the fields then take typed values.
 */
export interface Lookups {
	operations: string[];
	interactive: boolean;
	vendors: VendorSummary[];
	accounts: AccountSummary[];
	transformers: TransformerSummary[];
}

const NO_LOOKUPS: Lookups = {
	operations: [],
	interactive: false,
	vendors: [],
	accounts: [],
	transformers: []
};

export interface PublishBase {
	integration: Integration;
	/** The open draft (`has_draft`: it is `latest_version`), or null when there is none. */
	draft: IntegrationVersion | null;
}

// HUB-259 publish flow: the integration, its open draft (full spec) and the diff of the draft
// against the active version (shown before the confirm dialog, admin-console-ux §2).
export const load: PageLoad = async (event) => {
	const { access } = await event.parent();
	const api = consoleApi(event);
	const integration_id = event.params.integration_id;
	const integration = api.call((c) =>
		c.GET('/integrations/{integration_id}', { params: { path: { integration_id } } })
	);

	const base: Promise<ApiResult<PublishBase>> = integration.then(async (r) => {
		if (!r.ok) return r;
		const i = r.data;
		if (!i.has_draft || i.latest_version == null)
			return { ...r, data: { integration: i, draft: null } };
		const d = await api.call((c) =>
			c.GET('/integrations/{integration_id}/versions/{version}', {
				params: { path: { integration_id, version: i.latest_version! } }
			})
		);
		if (!d.ok) return d;
		return { ...r, data: { integration: i, draft: d.data.state === 'draft' ? d.data : null } };
	});

	const diff: Promise<ApiResult<VersionDiff> | null> = integration.then((r) => {
		if (
			!r.ok ||
			!r.data.has_draft ||
			r.data.active_version == null ||
			r.data.latest_version == null
		)
			return null;
		const { active_version: from, latest_version: to } = r.data;
		if (from === to) return null;
		return api.call((c) =>
			c.GET('/integrations/{integration_id}/diff', {
				params: { path: { integration_id }, query: { from, to } }
			})
		);
	});

	// Lookups for the structured editor, only for roles that can edit drafts (they read vendors,
	// accounts and transformers, which need the viewer role).
	const lookups: Promise<Lookups> = !can(access, 'updateIntegrationDraft')
		? Promise.resolve(NO_LOOKUPS)
		: integration.then(async (r) => {
				if (!r.ok) return NO_LOOKUPS;
				const capability = r.data.capability;
				const all = { limit: 200 } as const;
				const [cap, vendors, accounts, transformers] = await Promise.all([
					api.call((c) =>
						c.GET('/capabilities/{capability}', { params: { path: { capability } } })
					),
					collectPages((cursor) =>
						api.call((c) => c.GET('/vendors', { params: { query: { ...all, cursor } } }))
					),
					collectPages((cursor) =>
						api.call((c) => c.GET('/accounts', { params: { query: { ...all, cursor } } }))
					),
					collectPages((cursor) =>
						api.call((c) =>
							c.GET('/transformers', { params: { query: { ...all, capability, cursor } } })
						)
					)
				]);
				return {
					// Empty when the capability could not be read: the form then shows the operations
					// the spec itself lists.
					operations: cap.ok ? Object.keys(cap.data.operations) : [],
					interactive: cap.ok ? cap.data.interactive : false,
					vendors: vendors.ok ? vendors.data.items : [],
					accounts: accounts.ok ? accounts.data.items : [],
					transformers: transformers.ok ? transformers.data.items : []
				};
			});

	return { integrationId: integration_id, base, diff, lookups };
};

import type { ApiResult } from '$lib/api/client';
import { consoleApi } from '$lib/api/console';
import { toPlainError, type PlainApiError } from '$lib/api/actions';
import type { ListPage } from '$lib/api/list';
import type { TenantSummary } from '$lib/api/types';
import type { PageLoad } from './$types';
import { parseListState } from './filters';

/** A tenant of a tenant viewer's scope that `getTenant` did not return. */
export interface Unavailable {
	tenantId: string;
	error: PlainApiError;
}

// Tenants (HUB-270). `listTenants` needs the viewer role; a tenant viewer is below that (the
// contract: "a viewer restricted to the tenants in the token"), so for them the page reads each
// of their own tenants with `getTenant` (tenant_viewer) instead, and lists only those.
export const load: PageLoad = async (event) => {
	const state = parseListState(event.url.searchParams);
	const { access } = await event.parent();
	const api = consoleApi(event);
	if (!access.tenantScoped) {
		return {
			state,
			scoped: false,
			list: api.call((c) => c.GET('/tenants', { params: { query: state.query } })),
			unavailable: Promise.resolve([] as Unavailable[])
		};
	}
	const reads = Promise.all(
		access.tenants.map(async (tenantId) => ({
			tenantId,
			res: await api.call((c) =>
				c.GET('/tenants/{tenant_id}', { params: { path: { tenant_id: tenantId } } })
			)
		}))
	);
	const list: Promise<ApiResult<ListPage<TenantSummary>>> = reads.then((all) => {
		const q = state.q?.toLowerCase();
		const ok = all
			.flatMap((r) => (r.res.ok ? [r.res.data] : []))
			// The whole scope is in hand, so the search applies here (same fields as the API's `q`).
			.filter(
				(t) =>
					!q || t.tenant_id.toLowerCase().includes(q) || t.display_name.toLowerCase().includes(q)
			);
		const failed = all.find((r) => !r.res.ok);
		// Nothing readable: show that error; otherwise list what answered (failures listed apart).
		if (!ok.length && failed && !failed.res.ok) return failed.res;
		return {
			ok: true as const,
			status: 200,
			requestId: null,
			etag: null,
			data: { items: ok, next_cursor: null }
		};
	});
	const unavailable = reads.then((all) =>
		all.flatMap((r) =>
			!r.res.ok && all.some((x) => x.res.ok)
				? [{ tenantId: r.tenantId, error: toPlainError(r.res.error) }]
				: []
		)
	);
	return { state, scoped: true, list, unavailable };
};

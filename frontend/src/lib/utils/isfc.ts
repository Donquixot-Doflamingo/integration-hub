import type { IconName } from '$lib/components/icons';
import type { IsfcTenantLink, TenantSyncChange, TenantSyncResult } from '$lib/api/types';
import { formatUtc } from './format';

/**
 * Helpers for tenants that come from ISFC (HUB-485, contract 2.1.0). The `isfc` block is a
 * read-only copy written by the sync; the console never offers to edit it.
 */

export interface Tone {
	tone: 'ok' | 'warn' | 'danger' | 'neutral';
	icon: IconName;
	label: string;
}

/** The tenant's state in ISFC, in words: active, inactive, or no longer linked to the Hub client. */
export function isfcState(link: IsfcTenantLink): Tone {
	if (!link.linked) return { tone: 'warn', icon: 'alert', label: 'Unlinked in ISFC' };
	if (link.status === 'INACTIVE')
		return { tone: 'neutral', icon: 'pause', label: 'Inactive in ISFC' };
	return { tone: 'ok', icon: 'closed', label: 'Active in ISFC' };
}

/** True when ISFC, not the Hub, is why the tenant is disabled (the sync disables it). */
export function disabledInIsfc(link: IsfcTenantLink | null): boolean {
	return !!link && (!link.linked || link.status === 'INACTIVE');
}

/** "Last synced 2026-10-30 11:55 UTC", or "Never synced". */
export function lastSyncedText(syncedAt: string | null | undefined): string {
	return syncedAt ? formatUtc(syncedAt) : 'Never synced';
}

/** The most recent `synced_at` among tenants that have an ISFC link, or null. */
export function latestSync(items: readonly { isfc: IsfcTenantLink | null }[]): string | null {
	let best: string | null = null;
	for (const t of items) {
		const at = t.isfc?.synced_at;
		if (at && (best === null || Date.parse(at) > Date.parse(best))) best = at;
	}
	return best;
}

/** True when any listed tenant is managed by ISFC. */
export function hasIsfcTenants(items: readonly { isfc: IsfcTenantLink | null }[]): boolean {
	return items.some((t) => t.isfc !== null);
}

function plural(n: number, one: string, many = `${one}s`): string {
	return `${n} ${n === 1 ? one : many}`;
}

/** What the sync did, for the notice after "Sync now" on the list. */
export function syncSummary(r: TenantSyncResult): { message: string; tone: 'ok' | 'warn' } {
	if (r.result === 'failed') {
		return {
			message: `The sync could not finish: ${r.error ?? 'ISFC did not answer'}. The last good state is kept and nothing was disabled.`,
			tone: 'warn'
		};
	}
	const counts = `${plural(r.created, 'tenant')} created, ${r.updated} updated, ${r.disabled} disabled, ${r.unchanged} unchanged.`;
	return r.result === 'partial'
		? {
				message: `The sync finished with problems${r.error ? ` (${r.error})` : ''}: ${counts}`,
				tone: 'warn'
			}
		: { message: `Synced from ISFC: ${counts}`, tone: 'ok' };
}

/** What the sync did to one tenant, for the notice on its page. */
export function syncOneSummary(r: TenantSyncChange): { message: string; tone: 'ok' | 'warn' } {
	const id = r.tenant.tenant_id;
	if (r.result === 'failed') {
		return {
			message: `Could not refresh ${id} from ISFC: ${r.error ?? 'ISFC did not answer'}. Its last good state is kept.`,
			tone: 'warn'
		};
	}
	return r.result === 'partial'
		? {
				message: `Refreshed ${id} from ISFC with problems: ${r.error ?? 'see the change log'}.`,
				tone: 'warn'
			}
		: { message: `Refreshed ${id} from ISFC.`, tone: 'ok' };
}

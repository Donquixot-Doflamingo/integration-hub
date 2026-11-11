import { describe, expect, it } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import type { IsfcTenantLink } from '$lib/api/types';
import {
	disabledInIsfc,
	hasIsfcTenants,
	isfcState,
	lastSyncedText,
	latestSync,
	syncOneSummary,
	syncSummary
} from './isfc';

const link: IsfcTenantLink = {
	tenant_id: '6f1c0b2e-3a4d-4c5e-8f60-1a2b3c4d5e6f',
	key: 'ORDERS',
	name: 'Orders service',
	status: 'ACTIVE',
	linked: true,
	synced_at: '2026-10-30T11:55:00Z'
};

// HUB-485: tenants from ISFC.
describe('isfcState', () => {
	it('says active, inactive or unlinked in words', () => {
		expect(isfcState(link).label).toBe('Active in ISFC');
		expect(isfcState({ ...link, status: 'INACTIVE' }).label).toBe('Inactive in ISFC');
		expect(isfcState({ ...link, linked: false }).label).toBe('Unlinked in ISFC');
	});
	it('only inactive or unlinked tenants are disabled in ISFC', () => {
		expect(disabledInIsfc(null)).toBe(false);
		expect(disabledInIsfc(link)).toBe(false);
		expect(disabledInIsfc({ ...link, status: 'INACTIVE' })).toBe(true);
		expect(disabledInIsfc({ ...link, linked: false })).toBe(true);
	});
});

describe('last synced', () => {
	it('formats the time, or says never', () => {
		expect(lastSyncedText(link.synced_at)).toBe('2026-10-30 11:55 UTC');
		expect(lastSyncedText(null)).toBe('Never synced');
	});
	it('picks the most recent sync among ISFC tenants', () => {
		const items = [
			{ isfc: null },
			{ isfc: link },
			{ isfc: { ...link, synced_at: '2026-10-31T08:00:00Z' } }
		];
		expect(latestSync(items)).toBe('2026-10-31T08:00:00Z');
		expect(latestSync([{ isfc: null }])).toBeNull();
		expect(hasIsfcTenants(items)).toBe(true);
		expect(hasIsfcTenants([{ isfc: null }])).toBe(false);
	});
});

describe('sync summaries', () => {
	const ok = contractMocks.syncTenants.example;
	it('counts what the sync did', () => {
		expect(syncSummary(ok)).toEqual({
			tone: 'ok',
			message: 'Synced from ISFC: 1 tenant created, 0 updated, 0 disabled, 4 unchanged.'
		});
	});
	it('a failed sync is a warning that says nothing was disabled', () => {
		const s = syncSummary({ ...ok, result: 'failed', error: 'ISFC down' });
		expect(s.tone).toBe('warn');
		expect(s.message).toContain('ISFC down');
		expect(s.message).toContain('nothing was disabled');
	});
	it('a partial sync is a warning with the counts', () => {
		const s = syncSummary({ ...ok, result: 'partial', error: '1 tenant failed' });
		expect(s.tone).toBe('warn');
		expect(s.message).toContain('1 tenant failed');
		expect(s.message).toContain('4 unchanged');
	});
	it('one tenant', () => {
		const one = contractMocks.syncTenant.example;
		expect(syncOneSummary(one)).toEqual({
			tone: 'ok',
			message: 'Refreshed orders-service from ISFC.'
		});
		expect(syncOneSummary({ ...one, result: 'failed', error: 'ISFC down' }).tone).toBe('warn');
	});
});

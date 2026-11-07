import type { AccountSummary, TransformerSummary, VendorSummary } from '$lib/api/types';

/**
 * What the structured editor offers (HUB-292): vendors, accounts and transformers. Each list may
 * be empty when its call fails; the fields then take typed values. The operations come from the
 * spec itself (ADR-0023: the integration version owns them). Kept out of `+page.ts`, which may
 * only export what SvelteKit allows.
 */
export interface Lookups {
	vendors: VendorSummary[];
	accounts: AccountSummary[];
	transformers: TransformerSummary[];
}

export const NO_LOOKUPS: Lookups = { vendors: [], accounts: [], transformers: [] };

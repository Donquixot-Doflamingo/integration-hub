/** A tenant the operator can work in (ISFC `panels`): what the tenant switcher lists. */
export interface SwitchTarget {
	/** ISFC tenant UUID: what `X-Tenant-Id` carries. */
	id: string;
	key: string;
	name: string;
	/** The platform tenant: platform-wide resources (vendors, accounts, ...) live here. */
	platform: boolean;
}

/** `Name (KEY)`, with the platform tenant marked. */
export function tenantLabel(t: SwitchTarget): string {
	const base = t.name === t.key ? t.name : `${t.name} (${t.key})`;
	return t.platform ? `${base}, platform` : base;
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
/** ISFC tenant ids are UUIDs (`X-Tenant-Id` must be one, else the gateway answers `GW_016`). */
export const isTenantUuid = (v: unknown): v is string => typeof v === 'string' && UUID.test(v);

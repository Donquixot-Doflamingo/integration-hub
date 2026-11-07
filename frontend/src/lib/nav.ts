import type { Pathname } from '$app/types';
import type { IconName } from '$lib/components/icons';

/**
 * Top-level section paths. A literal union (not the whole generated `Pathname`) so `resolve(href)`
 * type-checks: TypeScript cannot match a call against a union of more than 25 route signatures.
 */
export type SectionHref = Extract<
	Pathname,
	| '/integrations'
	| '/health'
	| '/requests'
	| '/dlq'
	| '/transformers'
	| '/vendors'
	| '/accounts'
	| '/tenants'
	| '/snapshots'
	| '/change-log'
>;

export interface NavSection {
	href: SectionHref;
	label: string;
	/** One line under the page title: what the operator does here. */
	summary: string;
	icon: IconName;
}

/** Primary navigation, in on-call priority order (F-19 screens, HUB-254). */
export const sections: readonly NavSection[] = [
	{
		href: '/integrations',
		label: 'Integrations',
		summary: 'Every integration with its owner, capability, active version and targets.',
		icon: 'integrations'
	},
	{
		href: '/health',
		label: 'Health',
		summary:
			'Circuit breakers per vendor and account endpoint with parked requests and account lifecycle, open ones first.',
		icon: 'health'
	},
	{
		href: '/requests',
		label: 'Requests',
		summary: 'Recent requests across tenants. Open one to see its timeline.',
		icon: 'requests'
	},
	{
		href: '/dlq',
		label: 'DLQ',
		summary: 'Dead-lettered requests waiting for a replay or their retention to expire.',
		icon: 'dlq'
	},
	{
		href: '/transformers',
		label: 'Transformers',
		summary: 'Request and response transformers, their latest version and where they are used.',
		icon: 'transformers'
	},
	{
		href: '/vendors',
		label: 'Vendors',
		summary:
			'Vendor APIs the Hub calls: base URLs per region, auth scheme, defaults and endpoints.',
		icon: 'vendors'
	},
	{
		href: '/tenants',
		label: 'Tenants',
		summary: 'Calling teams, the integrations they own and their active API keys.',
		icon: 'tenants'
	},
	{
		href: '/snapshots',
		label: 'Snapshots',
		summary: 'Published registry snapshots that the gateway nodes load.',
		icon: 'snapshots'
	},
	{
		href: '/change-log',
		label: 'Change log',
		summary: 'Who changed what, when and why. Every mutation lands here with its reason.',
		icon: 'changelog'
	}
];

/** The section a pathname belongs to (`/integrations/int_x` -> Integrations), if any. */
export function sectionFor(pathname: string): NavSection | undefined {
	return sections.find((s) => pathname === s.href || pathname.startsWith(`${s.href}/`));
}

export interface Crumb {
	label: string;
	href: string;
}

/**
 * Segments that name a sub-collection and only make sense with the next one: `versions/8` becomes
 * one crumb "v8" (there is no page at `.../versions`).
 */
const PREFIXED: Record<string, (next: string) => string> = {
	versions: (v) => `v${v}`,
	endpoints: (ref) => ref
};
/** Readable labels for fixed sub-page segments. */
const LABELS: Record<string, string> = {
	diff: 'Compare',
	publish: 'Publish',
	new: 'New',
	edit: 'Edit',
	'new-version': 'New version'
};

/** Breadcrumb trail for a pathname: Console > Section > deeper segments (decoded). */
export function breadcrumbsFor(pathname: string): Crumb[] {
	const crumbs: Crumb[] = [{ label: 'Console', href: '/' }];
	const section = sectionFor(pathname);
	if (!section) return crumbs;
	crumbs.push({ label: section.label, href: section.href });
	const rest = pathname.slice(section.href.length).split('/').filter(Boolean);
	let href = section.href;
	for (let i = 0; i < rest.length; i++) {
		const segment = rest[i]!;
		const prefix = PREFIXED[segment];
		const nextSegment = rest[i + 1];
		if (prefix && nextSegment !== undefined) {
			href += `/${segment}/${nextSegment}`;
			crumbs.push({ label: prefix(decodeURIComponent(nextSegment)), href });
			i++;
			continue;
		}
		href += `/${segment}`;
		crumbs.push({ label: LABELS[segment] ?? decodeURIComponent(segment), href });
	}
	return crumbs;
}

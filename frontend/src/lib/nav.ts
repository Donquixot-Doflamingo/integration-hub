import type { Pathname } from '$app/types';
import type { IconName } from '$lib/components/icons';

export interface NavSection {
	href: Pathname;
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
		summary: 'Circuit breakers per account endpoint, open ones first.',
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

/** Breadcrumb trail for a pathname: Console > Section > deeper segments (as-is, decoded). */
export function breadcrumbsFor(pathname: string): Crumb[] {
	const crumbs: Crumb[] = [{ label: 'Console', href: '/' }];
	const section = sectionFor(pathname);
	if (!section) return crumbs;
	crumbs.push({ label: section.label, href: section.href });
	const rest = pathname.slice(section.href.length).split('/').filter(Boolean);
	let href = section.href;
	for (const segment of rest) {
		href += `/${segment}`;
		crumbs.push({ label: decodeURIComponent(segment), href });
	}
	return crumbs;
}

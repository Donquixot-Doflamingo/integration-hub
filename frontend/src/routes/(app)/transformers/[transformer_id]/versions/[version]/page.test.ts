import { render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import { accessFromRoles } from '$lib/auth/roles';
import { useActions } from '$lib/testing/forms';
import Page from './+page.svelte';
import { load } from './+page';
import { actions } from './+page.server';

// HUB-268: one stored transformer version.

vi.mock('$app/navigation', () => ({ invalidateAll: vi.fn(), goto: vi.fn() }));

function loadAt(version: string) {
	return load({
		fetch,
		url: new URL(`http://console.test/transformers/T1/versions/${version}`),
		params: { transformer_id: 'T1', version }
	} as never) as object;
}

describe('transformer version', () => {
	it('loads getTransformerVersion and shows source, docs link and goldens', async () => {
		useActions(actions, { params: { transformer_id: 'T1', version: '7' } });
		render(Page, {
			props: { data: { access: accessFromRoles(['editor']), ...loadAt('7') } as never }
		});
		expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('T1@7');
		expect(await screen.findByLabelText('T1@7 source (YAML)')).toBeInTheDocument();
		expect(screen.getByRole('link', { name: /developer\.clevertap\.com/ })).toBeInTheDocument();
		expect(screen.getByRole('button', { name: 'Run samples for T1@7' })).toBeInTheDocument();
		expect(screen.getByRole('link', { name: 'All versions of T1' })).toHaveAttribute(
			'href',
			'/transformers/T1'
		);
	});

	it.each(['0', 'seven', '1.5'])('answers 404 for version %j', (v) => {
		expect(() => loadAt(v)).toThrow();
	});
});

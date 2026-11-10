import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { HubApiError } from '$lib/api/errors';
import AccessUnavailable from './AccessUnavailable.svelte';

// HUB-455: when /me fails (not 401), the console says so and offers Retry, instead of showing an
// admin the pages of a tenant viewer.
const invalidateAll = vi.fn();
vi.mock('$app/navigation', () => ({ invalidateAll: () => invalidateAll() }));

describe('AccessUnavailable', () => {
	it('announces that the access could not be loaded and retries by reloading all data', async () => {
		const error = new HubApiError({
			status: 503,
			code: 'UNAVAILABLE',
			message: 'try later',
			retryable: true
		});
		render(AccessUnavailable, { props: { error } });
		expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent(/access/i);
		expect(screen.getByRole('alert')).toHaveTextContent('Could not load your access');
		await userEvent.click(screen.getByRole('button', { name: /retry/i }));
		expect(invalidateAll).toHaveBeenCalledOnce();
	});
});

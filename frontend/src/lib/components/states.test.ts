import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import type { ApiResult } from '$lib/api/client';
import { HubApiError } from '$lib/api/errors';
import AsyncHarness from './AsyncHarness.test.svelte';
import EmptyState from './EmptyState.svelte';
import ErrorPanel from './ErrorPanel.svelte';
import Skeleton from './Skeleton.svelte';

// HUB-255: shared loading, empty and error states.

type Item = { id: string };
const ok = (data: Item): ApiResult<Item> => ({
	ok: true,
	data,
	status: 200,
	requestId: null,
	etag: null
});
const fail = (error: HubApiError): ApiResult<Item> => ({
	ok: false,
	error,
	status: error.status,
	requestId: error.requestId
});
function deferred<T>() {
	let resolve!: (v: T) => void;
	const promise = new Promise<T>((r) => (resolve = r));
	return { promise, resolve };
}

describe('AsyncView', () => {
	it('shows a skeleton while loading, then the data', async () => {
		const d = deferred<ApiResult<Item>>();
		render(AsyncHarness, { props: { result: d.promise } });
		expect(screen.getByRole('status')).toHaveTextContent('Loading integration int_a...');
		d.resolve(ok({ id: 'int_a' }));
		expect(await screen.findByText('Item int_a')).toBeInTheDocument();
		expect(screen.queryByText(/Loading/)).toBeNull();
	});

	it('shows the error envelope with request id and retries', async () => {
		const user = userEvent.setup();
		const onretry = vi.fn();
		const error = new HubApiError({
			status: 404,
			code: 'NOT_FOUND',
			message: 'integration not found',
			requestId: 'req_01J9Z3M1A5B7C9D1E3F5G7H9KE'
		});
		render(AsyncHarness, { props: { result: Promise.resolve(fail(error)), onretry } });
		const alert = await screen.findByRole('alert');
		expect(within(alert).getByRole('heading')).toHaveTextContent(
			'Could not load integration int_a'
		);
		expect(alert).toHaveTextContent('NOT_FOUND');
		expect(alert).toHaveTextContent('req_01J9Z3M1A5B7C9D1E3F5G7H9KE');
		await user.click(within(alert).getByRole('button', { name: 'Retry' }));
		expect(onretry).toHaveBeenCalledOnce();
	});

	it('maps a rejected promise to a network error', async () => {
		render(AsyncHarness, { props: { result: Promise.reject(new TypeError('offline')) } });
		expect(await screen.findByRole('alert')).toHaveTextContent('NETWORK_ERROR');
	});

	it('keeps the previous data while a new call runs, and drops superseded answers', async () => {
		const first = deferred<ApiResult<Item>>();
		const { rerender } = render(AsyncHarness, { props: { result: first.promise } });
		first.resolve(ok({ id: 'one' }));
		await screen.findByText('Item one');

		const slow = deferred<ApiResult<Item>>();
		await rerender({ result: slow.promise });
		expect(screen.getByText('Item one')).toBeInTheDocument();
		expect(screen.getByText('Item one').closest('[aria-busy]')).toHaveAttribute(
			'aria-busy',
			'true'
		);
		expect(screen.getByRole('status')).toHaveTextContent('Updating integration int_a...');

		const fast = deferred<ApiResult<Item>>();
		await rerender({ result: fast.promise });
		fast.resolve(ok({ id: 'three' }));
		await screen.findByText('Item three');
		slow.resolve(ok({ id: 'two' }));
		await new Promise((r) => setTimeout(r, 0));
		expect(screen.queryByText('Item two')).toBeNull();
		expect(screen.getByText('Item three').closest('[aria-busy]')).toHaveAttribute(
			'aria-busy',
			'false'
		);
	});
});

describe('ErrorPanel', () => {
	it('says whether retrying helps, with the server delay', () => {
		const error = new HubApiError({
			status: 429,
			code: 'RATE_LIMITED',
			message: 'slow down',
			retryable: true,
			retryAfterMs: 2000,
			requestId: 'req_x'
		});
		render(ErrorPanel, { props: { error, context: 'Could not load DLQ' } });
		expect(screen.getByRole('alert')).toHaveTextContent(/Retry helps\s*Yes, after 2,000 ms/);
	});

	it('says retry does not help for a non-retryable error', () => {
		const error = new HubApiError({
			status: 403,
			code: 'FORBIDDEN',
			message: 'no',
			retryable: false
		});
		render(ErrorPanel, { props: { error, context: 'x' } });
		expect(screen.getByRole('alert')).toHaveTextContent('No, retrying gives the same answer');
	});

	it('lists validation details with their JSON pointers', () => {
		const error = new HubApiError({
			status: 400,
			code: 'VALIDATION_FAILED',
			message: 'Invalid request',
			details: [{ pointer: '/limit', message: 'must be <= 200' }]
		});
		render(ErrorPanel, { props: { error, context: 'Could not load integrations' } });
		const details = screen.getByRole('list', { name: 'Details' });
		expect(details).toHaveTextContent('/limit must be <= 200');
	});

	it('gives each panel its own heading id', () => {
		const error = new HubApiError({ status: 500, code: 'INTERNAL_ERROR', message: 'boom' });
		render(ErrorPanel, { props: { error, context: 'A' } });
		render(ErrorPanel, { props: { error, context: 'B', headingLevel: 3 } });
		const [a, b] = screen.getAllByRole('alert');
		expect(a).toHaveAccessibleName('A');
		expect(b).toHaveAccessibleName('B');
		expect(within(b!).getByRole('heading', { level: 3 })).toHaveTextContent('B');
	});
});

describe('EmptyState', () => {
	it('distinguishes "nothing exists" from "filters hide everything"', () => {
		const { unmount } = render(EmptyState, { props: { title: 'No integrations yet' } });
		expect(screen.getByRole('status')).toHaveTextContent('No integrations yet');
		expect(screen.queryByRole('link')).toBeNull();
		unmount();
		render(EmptyState, {
			props: { title: 'No integrations match', filtered: true, clearHref: '/integrations' }
		});
		expect(screen.getByRole('link', { name: 'Clear all filters' })).toHaveAttribute(
			'href',
			'/integrations'
		);
	});
});

describe('Skeleton', () => {
	it('is busy, decorative bars are hidden, and the label is announced', () => {
		const { container } = render(Skeleton, { props: { label: 'Loading versions...', lines: 3 } });
		expect(container.querySelector('[aria-busy="true"]')).not.toBeNull();
		expect(container.querySelectorAll('.bar[aria-hidden="true"]')).toHaveLength(3);
		expect(screen.getByRole('status')).toHaveTextContent('Loading versions...');
	});
});

describe('every console page uses the shared states', () => {
	const pages = import.meta.glob('/src/routes/\\(app\\)/**/+page.svelte', {
		query: '?raw',
		import: 'default',
		eager: true
	}) as Record<string, string>;
	// The home page is a static index of sections; it loads nothing.
	const STATIC = new Set(['/src/routes/(app)/+page.svelte']);

	it('finds the pages', () => {
		expect(Object.keys(pages).length).toBeGreaterThanOrEqual(9);
	});

	it.each(Object.keys(pages).filter((p) => !STATIC.has(p)))('%s', (path) => {
		const src = pages[path]!;
		expect(src).toMatch(
			/import (ListView|AsyncView) from '\$lib\/components\/(ListView|AsyncView)\.svelte'/
		);
		// No page renders its own {#await} or ad-hoc error text.
		expect(src).not.toMatch(/\{#await/);
	});

	it('error pages use the shared ErrorPage (ErrorPanel)', () => {
		const errorPages = import.meta.glob('/src/routes/**/+error.svelte', {
			query: '?raw',
			import: 'default',
			eager: true
		}) as Record<string, string>;
		expect(Object.keys(errorPages)).toHaveLength(2);
		for (const src of Object.values(errorPages)) {
			expect(src).toContain("import ErrorPage from '$lib/components/ErrorPage.svelte'");
		}
	});
});

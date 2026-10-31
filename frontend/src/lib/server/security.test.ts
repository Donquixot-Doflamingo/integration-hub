import type { RequestEvent } from '@sveltejs/kit';
import { describe, expect, it, vi } from 'vitest';
import { isDoubleSlashPath, rejectDoubleSlashPaths } from './security';

// HUB-279 / ADR-0009: workaround for GHSA-pfhg-q3qv-9q6r while the console stays on SvelteKit 2.x.

describe('isDoubleSlashPath', () => {
	it.each([
		{ path: '//evil.example/login', want: true },
		{ path: '//', want: true },
		{ path: '///integrations', want: true },
		{ path: '/', want: false },
		{ path: '/integrations', want: false },
		{ path: '/integrations//x', want: false },
		{ path: '/login', want: false }
	])('$path -> $want', ({ path, want }) => {
		expect(isDoubleSlashPath(path)).toBe(want);
	});
});

describe('rejectDoubleSlashPaths', () => {
	const run = (href: string) => {
		const resolve = vi.fn(async () => new Response('page'));
		const event = { url: new URL(href) } as RequestEvent;
		return { resolve, response: rejectDoubleSlashPaths({ event, resolve }) };
	};

	it('answers 404 without resolving the route', async () => {
		const { resolve, response } = run('http://console.test//evil.example/x');
		expect((await response).status).toBe(404);
		expect(resolve).not.toHaveBeenCalled();
	});

	it('passes normal paths through', async () => {
		const { resolve, response } = run('http://console.test/integrations?q=brand');
		expect(await (await response).text()).toBe('page');
		expect(resolve).toHaveBeenCalledOnce();
	});
});

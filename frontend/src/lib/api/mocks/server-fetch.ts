import type { RequestEvent } from '@sveltejs/kit';
import { getResponse } from 'msw';
import { adminApiBase } from '../console';
import { resolveBaseUrl } from '../client';
import { createHandlers } from './handlers';
import { SCENARIO_COOKIE, scenarioHandlers } from './scenarios';

/**
 * Mock mode on the server (`hooks.server.ts` `handleFetch`, mock builds only). The browser has
 * the MSW service worker, but form actions (every mutation, HUB-259+) call the admin API from the
 * SvelteKit server, where no worker runs. This answers those calls with the same handlers.
 * Returns undefined for anything that is not the admin API.
 */
export async function mockAdminFetch(
	event: Pick<RequestEvent, 'url' | 'cookies'>,
	request: Request
): Promise<Response | undefined> {
	const base = resolveBaseUrl(adminApiBase(), event.url.origin);
	if (request.url !== base && !request.url.startsWith(`${base}/`)) return undefined;
	const handlers = [...scenarioHandlers(event.cookies.get(SCENARIO_COOKIE)), ...createHandlers()];
	const response = await getResponse(handlers, request);
	return (
		response ??
		Response.json(
			{ error: { code: 'NOT_FOUND', message: 'No mock for this admin path', retryable: false } },
			{ status: 404 }
		)
	);
}

import { fireEvent, render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mockError, mockOperation, mswPath } from '$lib/api/mocks/handlers';
import { server } from '$lib/api/mocks/server';
import { accessFromRoles, type Access } from '$lib/auth/roles';
import { runAction, useActions } from '$lib/testing/forms';
import Page from './+page.svelte';
import { load } from './+page';
import { actions } from './+page.server';

// HUB-269: transformer dry-run: JSON checked client side, result as method, URL, headers, body,
// secrets exactly as the API redacted them, never logged.

vi.mock('$app/navigation', () => ({ invalidateAll: vi.fn(), goto: vi.fn() }));

const example = contractMocks.getTransformer.example;
const dryExample = contractMocks.dryRunTransformer.example;
const fixtureInput = example.latest.fixtures[0]!.input;

beforeEach(() => useActions(actions, { params: { transformer_id: 'T1' } }));
afterEach(() => vi.restoreAllMocks());

async function renderPage(access: Access = accessFromRoles(['editor'])) {
	const data = load({
		fetch,
		url: new URL('http://console.test/transformers/T1'),
		params: { transformer_id: 'T1' }
	} as never) as object;
	render(Page, { props: { data: { access, ...data } as never } });
	await screen.findByRole('heading', { name: 'Latest version: v7' });
	return userEvent.setup();
}

function capture(answer: unknown = dryExample) {
	const seen: Record<string, unknown>[] = [];
	server.use(
		http.post(mswPath('/transformers/dry-run'), async ({ request }) => {
			seen.push((await request.json()) as Record<string, unknown>);
			return HttpResponse.json(answer as never);
		})
	);
	return seen;
}

function neverCalled() {
	server.use(
		http.post(mswPath('/transformers/dry-run'), () => {
			throw new Error('must not be called');
		})
	);
}

const bodyField = () => screen.getByLabelText(/^Caller input \(JSON body\)/) as HTMLTextAreaElement;
const setBody = async (text: string) => {
	await fireEvent.input(bodyField(), { target: { value: text } });
};

describe('dry run', () => {
	it('starts from the first fixture and renders method, URL, headers and body', async () => {
		const seen = capture();
		const user = await renderPage();
		expect(JSON.parse(bodyField().value)).toEqual(fixtureInput);
		await user.click(screen.getByRole('button', { name: 'Dry-run T1@7' }));
		const result = await screen.findByRole('region', { name: /Dry run of T1@7/ });
		expect(seen).toEqual([{ ref: 'T1@7', input: fixtureInput }]);
		const req = within(result).getByRole('group', { name: 'Vendor request' });
		expect(req).toHaveTextContent('Method POST');
		expect(req).toHaveTextContent('URL https://in1.api.clevertap.com/1/upload');
		const rows = within(req)
			.getAllByRole('row')
			.slice(1)
			.map((r) => r.textContent);
		expect(rows).toEqual([
			'Content-Typeapplication/json',
			'X-CleverTap-Account-Id***',
			'X-CleverTap-Passcode***'
		]);
		expect(within(req).getByLabelText('Vendor request body')).toHaveTextContent(
			'"evtName": "order_placed"'
		);
		expect(result).toHaveTextContent('142 µs');
		expect(result).toHaveTextContent('Transformed');
	});

	it('sends the account and caller headers when given', async () => {
		const seen = capture();
		const user = await renderPage();
		await user.type(screen.getByLabelText(/^Account/), 'ct-brandA-in');
		await fireEvent.input(screen.getByLabelText(/^Caller headers/), {
			target: { value: '{"X-Request-Source": "checkout"}' }
		});
		await user.click(screen.getByRole('button', { name: 'Dry-run T1@7' }));
		await screen.findByRole('region', { name: /Dry run of T1@7/ });
		expect(seen[0]).toEqual({
			ref: 'T1@7',
			account_id: 'ct-brandA-in',
			input: fixtureInput,
			input_headers: { 'X-Request-Source': 'checkout' }
		});
	});

	it.each([
		['{ "user": { "id": "u_1" }, }', /The input is not valid JSON \(line 1, column \d+\)/],
		['{\n  "user": 1\n  "event": 2\n}', /The input is not valid JSON \(line 3, column \d+\)/],
		['[1, 2]', /must be a JSON object/],
		['', /The input is empty/]
	])('catches invalid input in the browser: %j', async (text, message) => {
		neverCalled();
		const user = await renderPage();
		await setBody(text);
		await user.click(screen.getByRole('button', { name: 'Dry-run T1@7' }));
		expect(await screen.findByText(message)).toBeInTheDocument();
		expect(bodyField()).toHaveAttribute('aria-invalid', 'true');
		expect(bodyField()).toHaveFocus();
		expect(screen.queryByRole('region', { name: /Dry run of/ })).toBeNull();
	});

	it('flags non-string caller headers and a bad account id before sending', async () => {
		neverCalled();
		const user = await renderPage();
		await user.type(screen.getByLabelText(/^Account/), 'x');
		await fireEvent.input(screen.getByLabelText(/^Caller headers/), {
			target: { value: '{"X-Trace": 1}' }
		});
		await user.click(screen.getByRole('button', { name: 'Dry-run T1@7' }));
		expect(
			await screen.findByText('The caller headers: "X-Trace" must be a string.')
		).toBeInTheDocument();
		expect(screen.getByText('Use an account id such as ct-brandA-in.')).toBeInTheDocument();
	});

	it('shows secrets only as returned, hides unmasked ones, and logs nothing', async () => {
		const logs = (['log', 'info', 'warn', 'error', 'debug'] as const).map((m) =>
			vi.spyOn(console, m).mockImplementation(() => {})
		);
		capture({
			...dryExample,
			vendor_request: {
				...dryExample.vendor_request!,
				headers: { 'X-CleverTap-Passcode': '***', Authorization: 'Bearer sk_live_leak' },
				body: { token: 'raw-token-leak', d: [] }
			}
		});
		const user = await renderPage();
		await user.click(screen.getByRole('button', { name: 'Dry-run T1@7' }));
		const result = await screen.findByRole('region', { name: /Dry run of T1@7/ });
		expect(result).toHaveTextContent('X-CleverTap-Passcode***');
		expect(result).toHaveTextContent('[redacted by console]');
		expect(document.body).not.toHaveTextContent('sk_live_leak');
		expect(document.body).not.toHaveTextContent('raw-token-leak');
		for (const spy of logs) {
			for (const call of spy.mock.calls) expect(JSON.stringify(call)).not.toMatch(/\*\*\*|leak/);
		}
	});

	it('lists compile errors with their line', async () => {
		capture({
			ok: false,
			kind: 'request',
			vendor_request: null,
			canonical_response: null,
			canonical_error: null,
			compile_errors: [{ line: 4, path: '/request/body', message: 'unknown field in.evt' }]
		});
		const user = await renderPage();
		await user.click(screen.getByRole('button', { name: 'Dry-run T1@7' }));
		const alert = await screen.findByRole('alert');
		expect(alert).toHaveTextContent('Compile errors');
		expect(alert).toHaveTextContent('line 4 /request/body unknown field in.evt');
		expect(screen.getByText('Failed')).toBeInTheDocument();
	});

	it('maps an API 400 pointer onto the input field', async () => {
		server.use(
			mockError('dryRunTransformer', 400, {
				details: [{ pointer: '/input/event', keyword: 'required', message: "missing 'event'" }]
			})
		);
		const user = await renderPage();
		await user.click(screen.getByRole('button', { name: 'Dry-run T1@7' }));
		expect(
			await screen.findByText("missing 'event'", { selector: 'p.field-error' })
		).toBeInTheDocument();
		expect(bodyField()).toHaveAttribute('aria-invalid', 'true');
		expect(screen.getByRole('alert')).toHaveTextContent('VALIDATION_FAILED');
	});

	it('a response transformer takes a vendor response and shows the canonical response', async () => {
		const seen = capture({
			ok: true,
			kind: 'response',
			vendor_request: null,
			canonical_response: { status: 'accepted', vendor_ref: 'ct-123' },
			canonical_error: null,
			compile_errors: [],
			duration_us: 40
		});
		const vendor_response = { status: 200, headers: {}, body: { status: 'success', processed: 1 } };
		server.use(
			mockOperation('getTransformer', {
				...example,
				transformer_id: 'R1',
				kind: 'response',
				latest: {
					...example.latest,
					transformer_id: 'R1',
					ref: 'R1@2',
					version: 2,
					kind: 'response',
					fixtures: [{ name: 'ok', vendor_response, expected_response: { status: 'accepted' } }]
				}
			})
		);
		const data = load({
			fetch,
			url: new URL('http://console.test/transformers/R1'),
			params: { transformer_id: 'R1' }
		} as never) as object;
		render(Page, { props: { data: { access: accessFromRoles(['editor']), ...data } as never } });
		const field = (await screen.findByLabelText(/^Vendor response \(JSON/)) as HTMLTextAreaElement;
		expect(JSON.parse(field.value)).toEqual(vendor_response);
		expect(screen.queryByLabelText(/^Caller headers/)).toBeNull();
		await userEvent.click(screen.getByRole('button', { name: 'Dry-run R1@2' }));
		expect(await screen.findByLabelText('Canonical response')).toHaveTextContent(
			'"vendor_ref": "ct-123"'
		);
		expect(seen[0]).toEqual({ ref: 'R1@2', vendor_response });
	});

	it('viewers get no dry run', async () => {
		await renderPage(accessFromRoles(['viewer']));
		expect(screen.queryByRole('button', { name: /Dry-run/ })).toBeNull();
		expect(screen.getByText('A dry run needs the Editor role.')).toBeInTheDocument();
	});
});

describe('dry-run action (no-JS path)', () => {
	const form = (fields: Record<string, string>) => {
		const f = new FormData();
		for (const [k, v] of Object.entries(fields)) f.set(k, v);
		return f;
	};

	it('checks the JSON again before calling the API', async () => {
		neverCalled();
		expect(
			await runAction(actions, 'dryRun', form({ ref: 'T1@7', mode: 'request', input: '{oops' }))
		).toMatchObject({
			type: 'failure',
			status: 400,
			data: { op: 'dryRun', fields: { input: expect.stringMatching(/not valid JSON/) } }
		});
		expect(
			await runAction(
				actions,
				'dryRun',
				form({ ref: 'T1@7', mode: 'response', vendor_response: '{"status": "ok"}' })
			)
		).toMatchObject({
			type: 'failure',
			data: {
				fields: { vendor_response: 'The vendor response: "status" must be an HTTP status code.' }
			}
		});
	});

	it('answers the result with no change id', async () => {
		capture();
		const r = await runAction(
			actions,
			'dryRun',
			form({ ref: 'T1@7', mode: 'request', input: '{}' })
		);
		expect(r).toMatchObject({ type: 'success', data: { op: 'dryRun', changeId: null } });
	});
});

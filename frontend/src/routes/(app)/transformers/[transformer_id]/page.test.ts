import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mockError, mockOperation, mswPath } from '$lib/api/mocks/handlers';
import { server } from '$lib/api/mocks/server';
import { accessFromRoles, type Access } from '$lib/auth/roles';
import { runAction, useActions } from '$lib/testing/forms';
import Page from './+page.svelte';
import { load } from './+page';
import { actions } from './+page.server';

// HUB-268: transformer viewer (header, docs link, highlighted source, fixtures, versions) and
// goldens with per-fixture diffs.

vi.mock('$app/navigation', () => ({ invalidateAll: vi.fn(), goto: vi.fn() }));

const example = contractMocks.getTransformer.example;

beforeEach(() => useActions(actions, { params: { transformer_id: 'T1' } }));

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

describe('transformer viewer', () => {
	it('shows the header and the vendor doc as a link', async () => {
		await renderPage();
		const head = screen.getByRole('region', { name: 'Transformer summary' });
		expect(head).toHaveTextContent('clevertap.upload@1');
		const doc = screen.getByRole('link', { name: /developer\.clevertap\.com/ });
		expect(doc).toHaveAttribute('href', example.latest.docs_url);
		expect(doc).toHaveAttribute('target', '_blank');
		expect(doc).toHaveAttribute('rel', expect.stringContaining('noopener'));
		expect(screen.getByText('Passing')).toBeInTheDocument();
	});

	it('does not turn a non-http docs_url into a link', async () => {
		server.use(
			mockOperation('getTransformer', {
				...example,
				latest: { ...example.latest, docs_url: 'javascript:alert(1)' }
			})
		);
		await renderPage();
		expect(screen.queryByRole('link', { name: /javascript/ })).toBeNull();
		expect(screen.getByText('javascript:alert(1)')).toBeInTheDocument();
	});

	it('renders the YAML source with line numbers and coloured keys, as text', async () => {
		server.use(
			mockOperation('getTransformer', {
				...example,
				latest: { ...example.latest, yaml: 'transformer: T1\nversion: 7\nnote: <b>bold</b>' }
			})
		);
		await renderPage();
		const source = screen.getByLabelText('T1@7 source (YAML)');
		expect(source.querySelectorAll('.line')).toHaveLength(3);
		expect(source.querySelector('.t-key')).toHaveTextContent('transformer');
		expect(source.querySelector('.t-number')).toHaveTextContent('7');
		expect(source.querySelector('b')).toBeNull();
		expect(source).toHaveTextContent('<b>bold</b>');
	});

	it('lists the samples with their parts, caller headers included', async () => {
		await renderPage();
		const fixture = screen.getByText('order_placed', { selector: 'summary code' });
		const details = fixture.closest('details')!;
		expect(within(details).getByLabelText('order_placed: Sample input body')).toHaveTextContent(
			'u_48213'
		);
		expect(within(details).getByLabelText('order_placed: Sample input headers')).toHaveTextContent(
			'X-Request-Source'
		);
		expect(
			within(details).getByLabelText('order_placed: Expected vendor request')
		).toHaveTextContent('/1/upload');
	});

	it('lists every version newest first, each linked', async () => {
		await renderPage();
		const history = await screen.findByRole('region', { name: 'Version history' });
		const links = within(history).getAllByRole('link');
		expect(links.map((l) => l.textContent)).toEqual(['T1@7', 'T1@6']);
		expect(links[1]).toHaveAttribute('href', '/transformers/T1/versions/6');
	});

	it('tells tenant viewers why the page is not for them', () => {
		const data = load({
			fetch,
			url: new URL('http://console.test/transformers/T1'),
			params: { transformer_id: 'T1' }
		} as never) as object;
		render(Page, {
			props: { data: { access: accessFromRoles(['tenant_viewer'], ['x-y-z']), ...data } as never }
		});
		expect(screen.getByRole('note')).toHaveTextContent('tenant viewers cannot see');
	});

	it('shows a 404 with its request id', async () => {
		server.use(mockError('getTransformer', 404));
		const data = load({
			fetch,
			url: new URL('http://console.test/transformers/T9'),
			params: { transformer_id: 'T9' }
		} as never) as object;
		render(Page, { props: { data: { access: accessFromRoles(['viewer']), ...data } as never } });
		const alert = await screen.findByRole('alert');
		expect(alert).toHaveTextContent('NOT_FOUND');
		expect(alert).toHaveTextContent(contractMocks.getTransformer.errors['404']!.error.request_id);
	});
});

describe('goldens', () => {
	it('viewers see the stored result and why they cannot run them', async () => {
		await renderPage(accessFromRoles(['viewer']));
		expect(screen.queryByRole('button', { name: /Run samples/ })).toBeNull();
		expect(screen.getByText(/Running samples needs the Editor role/)).toBeInTheDocument();
	});

	it('runs the goldens of this version and shows each fixture passing', async () => {
		const seen: unknown[] = [];
		server.use(
			http.post(mswPath('/transformers/run-goldens'), async ({ request }) => {
				seen.push(await request.json());
				return HttpResponse.json(contractMocks.runTransformerGoldens.example);
			})
		);
		const user = await renderPage();
		await user.click(screen.getByRole('button', { name: 'Run samples for T1@7' }));
		expect(await screen.findByText(/T1@7: 2 of 2 samples pass/)).toBeInTheDocument();
		expect(seen).toEqual([{ ref: 'T1@7' }]);
		expect(screen.getByText('All samples pass')).toBeInTheDocument();
		expect(screen.getAllByText('Output matches the expected one.')).toHaveLength(2);
	});

	it('renders the diff per failing fixture, failing ones first', async () => {
		server.use(
			mockOperation('runTransformerGoldens', {
				passed: false,
				total: 3,
				failed: 2,
				duration_ms: 12,
				results: [
					{ fixture: 'order_placed', passed: true, diff: [] },
					{
						fixture: 'no_timestamp',
						passed: false,
						diff: [
							{ op: 'replace', path: '/body/d/0/ts', before: 1793438100, after: null },
							{ op: 'add', path: '/body/d/0/extra', after: 'x' }
						]
					},
					{ fixture: 'bad_input', passed: false, diff: [], error: 'expr: unknown field in.evt' }
				]
			})
		);
		const user = await renderPage();
		await user.click(screen.getByRole('button', { name: 'Run samples for T1@7' }));
		expect(await screen.findByText(/1 of 3 samples pass, 2 failing/)).toBeInTheDocument();
		const items = screen.getAllByRole('listitem').filter((li) => li.querySelector('h4'));
		expect(items.map((li) => li.querySelector('h4 code')?.textContent)).toEqual([
			'no_timestamp',
			'bad_input',
			'order_placed'
		]);
		const diff = within(items[0]!).getByRole('region', {
			name: 'Differences for sample no_timestamp'
		});
		expect(
			within(diff)
				.getAllByRole('columnheader')
				.map((h) => h.textContent)
		).toEqual(['Path', 'Change', 'Expected', 'Actual']);
		expect(diff).toHaveTextContent('body.d[0].ts');
		expect(diff).toHaveTextContent('1793438100');
		expect(items[0]).toHaveTextContent('Failed');
		expect(items[1]).toHaveTextContent('Error: expr: unknown field in.evt');
	});

	it('shows a failed run with code and request id', async () => {
		server.use(mockError('runTransformerGoldens', 403));
		const user = await renderPage();
		await user.click(screen.getByRole('button', { name: 'Run samples for T1@7' }));
		const alert = await screen.findByRole('alert');
		expect(alert).toHaveTextContent('FORBIDDEN');
		expect(alert).toHaveTextContent(
			contractMocks.runTransformerGoldens.errors['403']!.error.request_id
		);
	});

	it('the action refuses a malformed ref without calling the API', async () => {
		server.use(
			http.post(mswPath('/transformers/run-goldens'), () => {
				throw new Error('must not be called');
			})
		);
		const f = new FormData();
		f.set('ref', 'T1@0');
		expect(await runAction(actions, 'runGoldens', f)).toMatchObject({
			type: 'failure',
			status: 400,
			data: { op: 'runGoldens', fields: { ref: expect.any(String) } }
		});
	});
});

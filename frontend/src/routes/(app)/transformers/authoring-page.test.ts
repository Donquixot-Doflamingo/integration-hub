import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { http, HttpResponse } from 'msw';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mockError, mswPath } from '$lib/api/mocks/handlers';
import { server } from '$lib/api/mocks/server';
import type { GoldensRunResult, Role } from '$lib/api/types';
import { accessFromRoles } from '$lib/auth/roles';
import { takeFlash } from '$lib/stores/flash.svelte';
import { useActions } from '$lib/testing/forms';
import VersionPage from './[transformer_id]/new-version/+page.svelte';
import { load as versionLoad } from './[transformer_id]/new-version/+page';
import { actions as versionActions } from './[transformer_id]/new-version/+page.server';
import NewPage from './new/+page.svelte';
import { load as newLoad } from './new/+page';
import ListPage from './+page.svelte';
import { load as listLoad } from './+page';

// HUB-293: create transformer versions from the console. Goldens of the exact draft gate
// "Create" (the API refuses failing goldens, 422), per-fixture diffs, masked dry run.

const goto = vi.fn();
vi.mock('$app/navigation', () => ({
	goto: (...a: unknown[]) => goto(...a),
	invalidateAll: vi.fn()
}));

const editor = accessFromRoles(['editor']);
const PASS: GoldensRunResult = contractMocks.runTransformerGoldens.example;
const FAIL: GoldensRunResult = {
	passed: false,
	total: 1,
	failed: 1,
	results: [
		{
			fixture: 'order_placed',
			passed: false,
			diff: [{ op: 'replace', path: '/path', before: '/1/wrong', after: '/1/upload' }],
			error: null
		}
	],
	duration_ms: 3
};

async function renderVersion(role: Role = 'editor') {
	const access = accessFromRoles([role]);
	const data = versionLoad({
		fetch,
		url: new URL('http://console.test/transformers/T1/new-version'),
		params: { transformer_id: 'T1' }
	} as never);
	render(VersionPage, { props: { data: { access, ...(data as object) } as never } });
}

/** Answers runTransformerGoldens with the given results in turn and records the bodies. */
function goldensAnswers(...answers: GoldensRunResult[]) {
	const bodies: Record<string, unknown>[] = [];
	server.use(
		http.post(mswPath('/transformers/run-goldens'), async ({ request }) => {
			bodies.push((await request.json()) as never);
			return HttpResponse.json(answers[Math.min(bodies.length - 1, answers.length - 1)]!);
		})
	);
	return bodies;
}

beforeEach(() => {
	goto.mockReset();
	takeFlash();
	useActions(versionActions, { params: { transformer_id: 'T1' } });
});

describe('new version of a transformer', () => {
	it('starts from the latest version; create stays locked until goldens of this draft pass', async () => {
		const bodies = goldensAnswers(FAIL, PASS);
		await renderVersion();
		const source = (await screen.findByLabelText(/^Source/)) as HTMLTextAreaElement;
		expect(source.value).toBe(contractMocks.getTransformer.example.latest.yaml);
		const create = screen.getByRole('button', { name: 'Create T1@8' });
		expect(create).toBeDisabled();
		expect(screen.getByText(/Run the goldens on this draft first/)).toBeInTheDocument();

		await userEvent.click(screen.getByRole('button', { name: 'Run goldens on this draft' }));
		expect(await screen.findByText('Goldens fail')).toBeInTheDocument();
		// The failing case with its diff (expected vs produced).
		const failed = screen
			.getAllByRole('listitem', { name: /order_placed/ })
			.find((l) => l.closest('.goldens'))!;
		expect(failed).toHaveTextContent('/1/wrong');
		expect(failed).toHaveTextContent('/1/upload');
		expect(create).toBeDisabled();
		expect(screen.getByText(/1 of 1 goldens fail/)).toBeInTheDocument();
		expect(bodies[0]).toMatchObject({
			draft: {
				kind: 'request',
				vendor_endpoint: 'clevertap.upload@1'
			},
			fixtures: [{ name: 'order_placed' }]
		});

		await userEvent.click(screen.getByRole('button', { name: 'Run goldens on this draft' }));
		expect(await screen.findByText('All goldens pass')).toBeInTheDocument();
		expect(create).toBeEnabled();

		// Any change to the draft locks it again until the goldens run on the new text.
		await userEvent.type(source, '\n# tweak');
		expect(create).toBeDisabled();
		expect(screen.getByText(/The draft changed since this run/)).toBeInTheDocument();
	});

	it('creates the version with a reason and the expected latest version, then opens it', async () => {
		goldensAnswers(PASS);
		const bodies: Record<string, unknown>[] = [];
		server.use(
			http.post(mswPath('/transformers/:transformer_id/versions'), async ({ request }) => {
				bodies.push((await request.json()) as never);
				return HttpResponse.json(contractMocks.createTransformerVersion.example, { status: 201 });
			})
		);
		await renderVersion();
		await userEvent.click(await screen.findByRole('button', { name: 'Run goldens on this draft' }));
		await screen.findByText('All goldens pass');
		await userEvent.click(screen.getByRole('button', { name: 'Create T1@8' }));
		const dialog = screen.getByRole('dialog', { name: 'Create T1@8' });
		await userEvent.type(within(dialog).getByLabelText(/^Reason/), 'Map context.campaign');
		await userEvent.click(within(dialog).getByRole('button', { name: 'Create T1@8' }));
		await waitFor(() => expect(goto).toHaveBeenCalledWith('/transformers/T1/versions/8'));
		expect(bodies[0]).toMatchObject({
			expected_latest_version: 7,
			yaml: contractMocks.getTransformer.example.latest.yaml,
			reason: 'Map context.campaign'
		});
		expect(takeFlash()).toMatchObject({
			changeId: contractMocks.createTransformerVersion.example.change_id,
			message: expect.stringContaining('Created T1@8')
		});
	});

	it('a 422 from the Hub (goldens failed there) keeps the dialog open with the report', async () => {
		goldensAnswers(PASS);
		server.use(
			mockError('createTransformerVersion', 422, {
				message: '1 publish check(s) failed',
				details: [{ keyword: 'goldens_pass', message: 'order_placed: golden mismatch: /path' }]
			})
		);
		await renderVersion();
		await userEvent.click(await screen.findByRole('button', { name: 'Run goldens on this draft' }));
		await screen.findByText('All goldens pass');
		await userEvent.click(screen.getByRole('button', { name: 'Create T1@8' }));
		const dialog = screen.getByRole('dialog', { name: 'Create T1@8' });
		await userEvent.type(within(dialog).getByLabelText(/^Reason/), 'Map context.campaign');
		await userEvent.click(within(dialog).getByRole('button', { name: 'Create T1@8' }));
		const alert = await within(dialog).findByRole('alert');
		expect(alert).toHaveTextContent('PUBLISH_VALIDATION_FAILED');
		expect(alert).toHaveTextContent('order_placed: golden mismatch');
		expect(goto).not.toHaveBeenCalled();
	});

	it('API pointers into the fixtures land on the fixture field', async () => {
		server.use(
			mockError('runTransformerGoldens', 400, {
				details: [
					{ pointer: '/fixtures/0/expected_request', message: 'expected_request must be an object' }
				]
			})
		);
		await renderVersion();
		await userEvent.click(await screen.findByRole('button', { name: 'Run goldens on this draft' }));
		const fixture = screen.getByRole('listitem', { name: /^Fixture 1/ });
		await waitFor(() =>
			expect(within(fixture).getByLabelText('Expected vendor request')).toHaveAttribute(
				'aria-invalid',
				'true'
			)
		);
	});

	it('dry-runs the draft and shows secrets as the API masked them', async () => {
		const bodies: Record<string, unknown>[] = [];
		server.use(
			http.post(mswPath('/transformers/dry-run'), async ({ request }) => {
				bodies.push((await request.json()) as never);
				return HttpResponse.json(contractMocks.dryRunTransformer.example);
			})
		);
		await renderVersion();
		await userEvent.type(await screen.findByLabelText(/^Account \(optional/), 'ct-brandA-in');
		await userEvent.click(screen.getByRole('button', { name: 'Dry run this draft' }));
		const dry = await screen.findByRole('region', { name: /Dry run of this draft/ });
		expect(within(dry).getByRole('row', { name: /X-CleverTap-Passcode/ })).toHaveTextContent('***');
		expect(bodies[0]).toMatchObject({
			draft: { kind: 'request', yaml: contractMocks.getTransformer.example.latest.yaml },
			account_id: 'ct-brandA-in',
			input: contractMocks.getTransformer.example.latest.fixtures[0]!.input
		});
		expect('ref' in bodies[0]!).toBe(false);
	});
});

describe('new transformer and roles', () => {
	it('a new id needs an id and the draft fields before goldens run', async () => {
		const bodies = goldensAnswers(PASS);
		const data = newLoad({ fetch, url: new URL('http://console.test/transformers/new') } as never);
		render(NewPage, { props: { data: { access: editor, ...(data as object) } as never } });
		await userEvent.click(await screen.findByRole('button', { name: 'Run goldens on this draft' }));
		expect(screen.getByLabelText(/^Transformer id/)).toHaveAttribute('aria-invalid', 'true');
		expect(
			screen
				.getAllByRole('alert')
				.some((a) => /Write the transformer source/.test(a.textContent ?? ''))
		).toBe(true);
		expect(bodies).toHaveLength(0);
		expect(screen.getByRole('button', { name: 'Create new@1' })).toBeDisabled();
	});

	it.each<Role>(['viewer', 'tenant_viewer'])('%s gets no authoring controls', async (role) => {
		await renderVersion(role);
		expect(screen.getByRole('note')).toHaveTextContent('needs the Editor role');
		expect(screen.queryByRole('button', { name: /Run goldens/ })).toBeNull();
		const access = accessFromRoles([role], role === 'tenant_viewer' ? ['orders-service'] : []);
		const data = listLoad({ fetch, url: new URL('http://console.test/transformers') } as never);
		render(ListPage, { props: { data: { access, ...(data as object) } as never } });
		expect(screen.queryByRole('link', { name: 'New transformer' })).toBeNull();
	});

	it('editors get "New transformer" on the list', () => {
		const data = listLoad({ fetch, url: new URL('http://console.test/transformers') } as never);
		render(ListPage, { props: { data: { access: editor, ...(data as object) } as never } });
		expect(screen.getByRole('link', { name: 'New transformer' })).toHaveAttribute(
			'href',
			'/transformers/new'
		);
	});
});

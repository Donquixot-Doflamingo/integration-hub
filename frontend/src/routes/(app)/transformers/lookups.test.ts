import { http, HttpResponse } from 'msw';
import { describe, expect, it } from 'vitest';
import { consoleApi } from '$lib/api/console';
import { contractMocks } from '$lib/api/mocks/contract.gen';
import { mswPath } from '$lib/api/mocks/handlers';
import { server } from '$lib/api/mocks/server';
import { authoringLookups } from './lookups';

// HUB-455: every vendor's endpoints are read (not the first 25), and a vendor that cannot be read
// is reported, not skipped silently.
const api = () => consoleApi({ fetch, url: new URL('http://console.test/transformers/new') });
const vendorExample = contractMocks.getVendor.example;
const summary = contractMocks.listVendors.example.items[0]!;

function vendors(n: number, failing: string[] = []) {
	const ids = Array.from({ length: n }, (_, i) => `vendor-${String(i).padStart(3, '0')}`);
	server.use(
		http.get(mswPath('/vendors'), () =>
			HttpResponse.json({
				items: ids.map((vendor_id) => ({ ...summary, vendor_id })),
				next_cursor: null
			})
		),
		http.get(mswPath('/vendors/:vendor_id'), ({ params }) =>
			failing.includes(String(params.vendor_id))
				? HttpResponse.json(contractMocks.getVendor.errors['404'] as never, { status: 404 })
				: HttpResponse.json({ ...vendorExample, vendor_id: params.vendor_id } as never)
		)
	);
	return ids;
}

describe('authoringLookups', () => {
	it('reads the endpoints of every vendor, past the first 25', async () => {
		const ids = vendors(30);
		const r = await authoringLookups(api());
		expect(r.ok).toBe(true);
		if (!r.ok) return;
		expect(r.data.problems).toEqual([]);
		const last = ids[29]!;
		expect(r.data.endpointRefs.some((ref) => ref.startsWith(`${last}.`))).toBe(true);
	});

	it('names a vendor whose endpoints could not be read', async () => {
		vendors(3, ['vendor-001']);
		const r = await authoringLookups(api());
		expect(r.ok).toBe(true);
		if (!r.ok) return;
		expect(r.data.problems).toHaveLength(1);
		expect(r.data.problems[0]).toContain('vendor-001');
		expect(r.data.endpointRefs.some((ref) => ref.startsWith('vendor-000.'))).toBe(true);
	});
});

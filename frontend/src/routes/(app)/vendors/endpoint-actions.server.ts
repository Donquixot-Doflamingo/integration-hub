import type { RequestEvent } from '@sveltejs/kit';
import {
	actionApi,
	finishByPointer,
	invalid,
	readReason,
	requireSession
} from '$lib/server/actions';
import { createEndpointBody, endpointValuesFromForm, ENDPOINT_POINTERS } from './endpoint-form';

/**
 * `createVendorEndpoint` (admin), shared by the vendor page and the endpoint page (HUB-290). It
 * creates `<name>@1` for a new name or `<name>@<n+1>` for an existing one: versions are immutable.
 */
export async function createEndpoint(event: RequestEvent<{ vendor_id: string }>) {
	requireSession(event);
	const form = await event.request.formData();
	const reason = readReason('createEndpoint', form);
	if (!reason.ok) return reason.failure;
	const built = createEndpointBody(endpointValuesFromForm(form), reason.reason);
	if (!built.ok) return invalid('createEndpoint', built.fields);
	const res = await actionApi(event).call((c) =>
		c.POST('/vendors/{vendor_id}/endpoints', {
			params: { path: { vendor_id: event.params.vendor_id } },
			body: built.body
		})
	);
	return finishByPointer('createEndpoint', res, ENDPOINT_POINTERS);
}

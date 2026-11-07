import type { Actions } from './$types';
import { createEndpoint } from '../../../endpoint-actions.server';

// HUB-290, HUB-436: createVendorEndpoint (admin), with the endpoint's request schema.
export const actions = { createEndpoint } satisfies Actions;

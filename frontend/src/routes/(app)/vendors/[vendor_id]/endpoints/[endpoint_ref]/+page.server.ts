import type { Actions } from './$types';
import { createEndpoint } from '../../../endpoint-actions.server';

// HUB-290: create the next version of this endpoint (createVendorEndpoint, admin).
export const actions = { createEndpoint } satisfies Actions;

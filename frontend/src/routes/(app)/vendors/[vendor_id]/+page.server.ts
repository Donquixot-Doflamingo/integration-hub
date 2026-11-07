import type { Actions } from './$types';
import { createEndpoint } from '../endpoint-actions.server';

// HUB-290: add an endpoint or a new endpoint version (createVendorEndpoint, admin).
export const actions = { createEndpoint } satisfies Actions;

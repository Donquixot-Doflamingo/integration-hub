import type { Actions } from './$types';
import { transformerActions } from '../actions.server';

// HUB-268 / HUB-269: goldens and dry-run for the latest version.
export const actions = { ...transformerActions } satisfies Actions;

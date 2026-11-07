import type { Actions } from './$types';
import { authoringActions } from '../authoring.server';

// HUB-293: goldens and dry run of the draft, then createTransformerVersion (version 1).
export const actions = { ...authoringActions } satisfies Actions;

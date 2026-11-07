import type { Actions } from './$types';
import { authoringActions } from '../../authoring.server';

// HUB-293: goldens and dry run of the draft, then createTransformerVersion (n+1, with
// expected_latest_version so a concurrent version answers 409).
export const actions = { ...authoringActions } satisfies Actions;

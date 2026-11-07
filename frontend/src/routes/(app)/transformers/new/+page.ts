import { consoleApi } from '$lib/api/console';
import type { PageLoad } from './$types';
import { authoringLookups } from '../lookups';

// New transformer (HUB-293): capabilities and vendor endpoints for the form.
export const load: PageLoad = (event) => ({ lookups: authoringLookups(consoleApi(event)) });

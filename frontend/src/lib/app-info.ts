/** Product name shown in the shell and page titles. */
export const appName = 'Integration Hub console';

/** Builds a document title for a section, e.g. `pageTitle('DLQ')` -> "DLQ - Integration Hub console". */
export function pageTitle(section?: string): string {
	const trimmed = section?.trim();
	return trimmed ? `${trimmed} - ${appName}` : appName;
}

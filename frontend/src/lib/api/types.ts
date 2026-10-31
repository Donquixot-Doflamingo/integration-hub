import type { components, operations } from './schema';

/**
 * Friendly aliases for generated contract types. This is the ONLY place outside `schema.d.ts`
 * where API type names are declared (enforced by `scripts/check-api-types.mjs`).
 * Add an alias here when a screen needs one; never redeclare fields.
 */
export type Schemas = components['schemas'];
export type Operations = operations;

/** Success (2xx) JSON body of an operation, by operationId. */
export type OperationResponse<Op extends keyof operations> =
	operations[Op]['responses'] extends infer R
		? {
				[S in keyof R]: S extends 200 | 201 | 202
					? R[S] extends { content: { 'application/json': infer B } }
						? B
						: never
					: never;
			}[keyof R]
		: never;

export type Me = Schemas['Me'];
export type Role = Schemas['Role'];
export type BreakerState = Schemas['BreakerState'];
export type RequestState = Schemas['RequestState'];
export type Slug = Schemas['Slug'];

/** Identity a console session carries (subset of `Me`; no token, safe to send to the browser). */
export type SessionUser = Pick<Me, 'subject' | 'email' | 'name' | 'roles' | 'tenants'>;

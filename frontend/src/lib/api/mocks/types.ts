import type { AdminErrorEnvelope } from '../errors';
import type { OperationRequestBody, OperationResponse, Operations } from '../types';

export type OperationId = keyof Operations;
export type HttpMethodName = 'get' | 'put' | 'post' | 'patch' | 'delete';

/** One operation's mock, extracted from the contract examples (see contract.gen.ts). */
export interface OperationMock<Op extends OperationId> {
	method: HttpMethodName;
	/** Contract path relative to `/admin/v1`, with `{param}` placeholders. */
	path: string;
	/** Success status from the contract (lowest declared 2xx). */
	status: number;
	/** Example response headers declared by the contract (e.g. ETag). */
	headers: Record<string, string>;
	/** The success example, typed by the generated client. */
	example: OperationResponse<Op>;
	/** Example error envelope per declared 4xx/5xx status. */
	errors: Partial<Record<string, AdminErrorEnvelope>>;
	/** Named examples (`examples:` in the contract), by name; typed by the generated client. */
	named?: {
		request?: Record<string, OperationRequestBody<Op>>;
		response?: Record<string, OperationResponse<Op>>;
		/** status -> example name -> error envelope. */
		errors?: Record<string, Record<string, AdminErrorEnvelope>>;
	};
}

/** Every admin operation must have a mock: a new operationId without one fails `npm run check`. */
export type ContractMocks = { [Op in OperationId]: OperationMock<Op> };

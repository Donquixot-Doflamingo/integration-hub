import { claimingPointer } from '$lib/api/actions';
import type {
	DeliveryMode,
	IntegrationOperation,
	IntegrationSpec,
	Target,
	TransformerKind,
	TransformerPair,
	TransformerSummary
} from '$lib/api/types';
import type { SpecProblem } from './draft';

/**
 * The structured draft editor (HUB-292, HUB-436): the form edits the same `IntegrationSpec` the
 * YAML editor does. Operations (ADR-0023: each with the caller's request schema), targets (vendor,
 * account, weight, default transformer pair and per-operation pairs, `TargetRef.operations`; the
 * response transformer is optional, none = pass-through), delivery and audit have fields;
 * failover, breaker, webhooks, shadow and target `params` are kept as they are and edited in YAML. Every field is named by its JSON
 * pointer into the spec, so publish-check and validation pointers land on the field they name.
 */
export const DELIVERY_MODES: readonly DeliveryMode[] = ['sync', 'wait', 'async', 'async_callback'];
export const deliveryLabels: Record<DeliveryMode, string> = {
	sync: 'Sync: the caller waits for the vendor',
	wait: 'Wait: answer within the window, else 202',
	async: 'Async: 202 at once, result by polling',
	async_callback: 'Async with callback'
};
export const ON_CIRCUIT_OPEN = ['fail_fast', 'failover', 'queue_if_allowed'] as const;
export const PENDING = ['re_resolve', 'pinned'] as const;
export const PAYLOADS = ['full', 'masked', 'sealed', 'metadata_only'] as const;

/** Contract `TransformerRef`: `<transformer_id>@<version>`. */
export const TRANSFORMER_REF = /^[A-Za-z][A-Za-z0-9_-]{0,63}@[1-9][0-9]*$/;

/** Contract `OperationName`. Single-operation integrations use `default`. */
export const OPERATION_NAME = /^[a-z][a-z0-9_]{0,31}$/;
export const DEFAULT_OPERATION = 'default';

/** A new operation: no fields yet, live calls checked against the schema (the contract default). */
export function newOperation(): IntegrationOperation {
	return {
		idempotent: false,
		validate_input: true,
		request_schema: { body: { type: 'object', properties: {} } }
	};
}

const isObject = (v: unknown): v is Record<string, unknown> =>
	!!v && typeof v === 'object' && !Array.isArray(v);

/** The spec as the form edits it: every section the form shows exists. */
export function forForm(value: Record<string, unknown>): IntegrationSpec {
	const spec = structuredClone(value) as Partial<IntegrationSpec>;
	return {
		...spec,
		operations: isObject(spec.operations) ? spec.operations : {},
		targets: Array.isArray(spec.targets) ? spec.targets : [],
		delivery: spec.delivery && typeof spec.delivery === 'object' ? spec.delivery : { mode: 'wait' },
		audit: spec.audit && typeof spec.audit === 'object' ? spec.audit : { payloads: 'masked' }
	} as IntegrationSpec;
}

/** A spec to start a first draft from (no active version yet). */
export function starterSpec(interactive: boolean): IntegrationSpec {
	return {
		operations: { [DEFAULT_OPERATION]: newOperation() },
		targets: [],
		delivery: interactive
			? { mode: 'sync', on_circuit_open: 'fail_fast' }
			: { mode: 'wait', wait_ms: 3000, on_circuit_open: 'fail_fast' },
		audit: { payloads: 'masked' }
	};
}

export function emptyTarget(weight: number): Target {
	return {
		vendor_id: '',
		account_id: '',
		transformer: { request: '', response: null },
		weight
	};
}

export function weightsTotal(spec: IntegrationSpec): number {
	return spec.targets.reduce((n, t) => n + (Number.isFinite(t.weight) ? t.weight : 0), 0);
}

/** Sections the form does not edit but the spec has (kept as they are; edited in YAML). */
export function yamlOnlySections(spec: IntegrationSpec): string[] {
	const out: string[] = [];
	if (spec.failover?.length) out.push(`failover (${spec.failover.length})`);
	if (spec.breaker && Object.keys(spec.breaker).length) out.push('breaker override');
	if (spec.webhooks?.length) out.push(`webhooks (${spec.webhooks.length})`);
	if (spec.shadow) out.push('shadow');
	if (spec.targets.some((t) => t.params && Object.keys(t.params).length)) out.push('target params');
	return out;
}

/** Operation names of the spec, in the order it lists them. */
export function operationNames(spec: IntegrationSpec): string[] {
	return Object.keys(spec.operations ?? {});
}

/** Operation names a target lists that the spec does not declare (to be removed). */
export function unknownOperations(target: Target, operations: readonly string[]): string[] {
	return Object.keys(target.operations ?? {}).filter((op) => !operations.includes(op));
}

/** Gives `op` its own pair (starting from the default pair) or drops it back to the default. */
export function setOwnPair(target: Target, op: string, own: boolean): void {
	if (own) {
		const ops = (target.operations ??= {});
		ops[op] ??= { request: target.transformer.request, response: target.transformer.response };
	} else if (target.operations) {
		delete target.operations[op];
		if (!Object.keys(target.operations).length) delete target.operations;
	}
}

/** Every pointer a field of the form answers for, given the spec and its operations. */
export function fieldPointers(spec: IntegrationSpec, operations: readonly string[]): string[] {
	const out = [
		'/operations',
		'/targets',
		'/delivery',
		'/delivery/mode',
		'/delivery/wait_ms',
		'/delivery/ttl_ms'
	];
	out.push('/delivery/on_circuit_open', '/delivery/pending', '/delivery/retry');
	out.push('/delivery/retry/max_attempts', '/delivery/retry/schedule_ms');
	out.push('/audit', '/audit/payloads', '/audit/retention_ms');
	spec.targets.forEach((t, i) => {
		const p = `/targets/${i}`;
		out.push(p, `${p}/vendor_id`, `${p}/account_id`, `${p}/weight`);
		out.push(`${p}/transformer`, `${p}/transformer/request`, `${p}/transformer/response`);
		out.push(`${p}/operations`);
		for (const op of new Set([...operations, ...Object.keys(t.operations ?? {})])) {
			const o = `${p}/operations/${escapeToken(op)}`;
			out.push(o, `${o}/request`, `${o}/response`);
		}
	});
	return out;
}

/** RFC 6901 token escaping. */
export function escapeToken(token: string): string {
	return token.replace(/~/g, '~0').replace(/\//g, '~1');
}

/**
 * Problems by the field that shows them (the longest field pointer that is a prefix of the
 * problem's pointer), and the ones no field claims (failover, webhooks, ...: shown in a summary
 * that points to the YAML editor).
 */
export function problemsByField(
	problems: readonly SpecProblem[],
	pointers: readonly string[]
): { byField: Map<string, string[]>; unclaimed: SpecProblem[] } {
	const byField = new Map<string, string[]>();
	const unclaimed: SpecProblem[] = [];
	for (const p of problems) {
		const field = claimingPointer(p.pointer, pointers);
		if (!field) unclaimed.push(p);
		else byField.set(field, [...(byField.get(field) ?? []), p.message]);
	}
	return { byField, unclaimed };
}

/** Problems the console sees before saving (the API checks everything again). */
export function formProblems(spec: IntegrationSpec, operations: readonly string[]): SpecProblem[] {
	const out: SpecProblem[] = [];
	if (!operations.length)
		out.push({ pointer: '/operations', message: 'Add at least one operation.' });
	if (!spec.targets.length) out.push({ pointer: '/targets', message: 'Add at least one target.' });
	spec.targets.forEach((t, i) => {
		const p = `/targets/${i}`;
		if (!t.vendor_id) out.push({ pointer: `${p}/vendor_id`, message: 'Pick the vendor.' });
		if (!t.account_id) out.push({ pointer: `${p}/account_id`, message: 'Pick the account.' });
		if (!Number.isInteger(t.weight) || t.weight < 0 || t.weight > 100)
			out.push({ pointer: `${p}/weight`, message: 'A whole number from 0 to 100.' });
		pairProblems(t.transformer, `${p}/transformer`, out);
		for (const [op, pair] of Object.entries(t.operations ?? {})) {
			const o = `${p}/operations/${escapeToken(op)}`;
			if (!operations.includes(op))
				out.push({ pointer: o, message: `The spec has no operation "${op}". Remove it.` });
			pairProblems(pair, o, out);
		}
	});
	if (spec.targets.length && weightsTotal(spec) !== 100)
		out.push({
			pointer: '/targets',
			message: `Weights total ${weightsTotal(spec)}; they must total 100.`
		});
	const d = spec.delivery;
	if (d.mode === 'wait' && d.wait_ms != null && (!Number.isInteger(d.wait_ms) || d.wait_ms < 0))
		out.push({ pointer: '/delivery/wait_ms', message: 'A whole number of milliseconds.' });
	return out;
}

/** The request transformer is required; the response transformer is optional (pass-through). */
function pairProblems(pair: TransformerPair | undefined, ptr: string, out: SpecProblem[]) {
	const request = pair?.request ?? '';
	if (!request) out.push({ pointer: `${ptr}/request`, message: 'Pick the request transformer.' });
	else if (!TRANSFORMER_REF.test(request))
		out.push({ pointer: `${ptr}/request`, message: 'A transformer version such as T1@7.' });
	const response = pair?.response ?? '';
	if (response && !TRANSFORMER_REF.test(response))
		out.push({
			pointer: `${ptr}/response`,
			message: 'A transformer version such as R1@2, or empty to pass the vendor body through.'
		});
}

/**
 * Transformer versions to offer for a target, of the wanted kind, each at its latest version
 * first and then older ones (`T1@7`, `T1@6`, ...; at most `depth`). Request transformers are those
 * of the target's vendor (their `vendor_endpoint` starts with `<vendor>.`); response transformers
 * stand alone (no endpoint), so all of them are offered.
 */
export function transformerOptions(
	transformers: readonly TransformerSummary[],
	kind: TransformerKind,
	vendorId: string,
	depth = 3
): string[] {
	return transformers
		.filter(
			(t) =>
				t.kind === kind &&
				(kind !== 'request' || !vendorId || !!t.vendor_endpoint?.startsWith(`${vendorId}.`))
		)
		.flatMap((t) =>
			Array.from(
				{ length: Math.min(depth, t.latest_version) },
				(_, i) => `${t.transformer_id}@${t.latest_version - i}`
			)
		);
}

/** "3000, 10000" -> [3000, 10000]; null when it is not a comma-separated list of whole numbers. */
export function parseSchedule(text: string): number[] | null {
	const parts = text
		.split(',')
		.map((s) => s.trim().replace(/_/g, ''))
		.filter(Boolean);
	if (!parts.every((p) => /^\d+$/.test(p))) return null;
	return parts.map(Number);
}

<script lang="ts">
	import CodeEditor from '$lib/components/CodeEditor.svelte';
	import type { SpecProblem } from './draft';

	/**
	 * YAML (or JSON) editor for an integration spec (HUB-259): `CodeEditor` with the spec's label,
	 * hint and problem heading. Problems are JSON pointers into the spec.
	 */
	let {
		value = $bindable(''),
		problems = [],
		error = null,
		disabled = false
	}: {
		value?: string;
		problems?: SpecProblem[];
		/** A whole-field problem (YAML syntax), e.g. "Line 3: ...". */
		error?: string | null;
		disabled?: boolean;
	} = $props();
</script>

<CodeEditor
	bind:value
	label="Spec"
	labelNote="(YAML or JSON)"
	name="spec"
	hint="Targets, failover, delivery, breaker, webhooks, audit and shadow, as in the active version above. Durations are in milliseconds. Weights must total 100."
	problemsTitle="Fix the spec before publishing"
	{problems}
	{error}
	{disabled}
/>

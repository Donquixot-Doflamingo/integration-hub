<script lang="ts">
	import type { ApiResult } from '$lib/api/client';
	import AsyncView from './AsyncView.svelte';

	let { result, onretry }: { result: Promise<ApiResult<{ id: string }>>; onretry?: () => void } =
		$props();
</script>

<AsyncView {result} noun="integration int_a" {onretry}>
	{#snippet children(item)}<p>Item {item.id}</p>{/snippet}
</AsyncView>

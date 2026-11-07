<script lang="ts">
	import type { FieldErrors } from '$lib/api/actions';
	import FieldError from '$lib/components/FieldError.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import {
		AUTH_SCHEMES,
		authSchemeLabels,
		BREAKER_FIELDS,
		DISPLAY_NAME_MAX,
		NAMES,
		problems,
		WEBHOOK_SCHEMES,
		webhookSchemeLabels,
		type FormValues
	} from './vendor-form';

	/**
	 * Vendor create / edit inputs (HUB-290). The values are checked against the contract as the
	 * operator types (`vendor-form.ts`); a field shows its problem once it was left (or after a
	 * submit attempt, `showAll`), and API pointers (`errors`) win.
	 */
	let {
		mode,
		values = $bindable(),
		errors = {},
		showAll = false,
		endpointRefs = [],
		disabled = false
	}: {
		mode: 'create' | 'edit';
		values: FormValues;
		/** From the action: API details by pointer, or the server's own checks. */
		errors?: FieldErrors;
		showAll?: boolean;
		/** The vendor's endpoint refs, offered for the health probe (edit). */
		endpointRefs?: string[];
		disabled?: boolean;
	} = $props();

	const id = $props.id();
	let touched = $state<Record<string, boolean>>({});
	const found = $derived(problems(values, mode));

	function err(ptr: string): string | null {
		return errors[ptr] ?? (touched[ptr] || showAll ? (found[ptr] ?? null) : null);
	}
	const fid = (ptr: string) => `${id}${ptr.replaceAll('/', '-')}`;
	const describedBy = (ptr: string, hint = false) =>
		[hint ? `${fid(ptr)}-hint` : '', err(ptr) ? `${fid(ptr)}-error` : '']
			.filter(Boolean)
			.join(' ') || undefined;
	const leave = (ptr: string) => () => (touched[ptr] = true);

	function addRegion() {
		values.regions.push({ code: '', url: '' });
	}
	function removeRegion(i: number) {
		values.regions.splice(i, 1);
		if (!values.regions.length) values.regions.push({ code: '', url: '' });
	}
</script>

<fieldset class="fk-group" {disabled}>
	<legend>Vendor</legend>
	<div class="fk-grid">
		{#if mode === 'create'}
			<div class="fk-field">
				<label for={fid('/vendor_id')}>Vendor id <span class="fk-req">(required)</span></label>
				<input
					id={fid('/vendor_id')}
					name="vendor_id"
					bind:value={values.vendor_id}
					onblur={leave('/vendor_id')}
					maxlength="64"
					autocomplete="off"
					spellcheck="false"
					placeholder="clevertap"
					aria-invalid={err('/vendor_id') ? 'true' : undefined}
					aria-describedby={describedBy('/vendor_id', true)}
				/>
				<p id="{fid('/vendor_id')}-hint" class="fk-hint">
					Permanent. 3 to 64 letters, digits, "_" or "-". Callers never see it.
				</p>
				<FieldError id="{fid('/vendor_id')}-error" message={err('/vendor_id')} />
			</div>
		{/if}
		<div class="fk-field">
			<label for={fid('/display_name')}>Display name <span class="fk-req">(required)</span></label>
			<input
				id={fid('/display_name')}
				name="display_name"
				bind:value={values.display_name}
				onblur={leave('/display_name')}
				maxlength={DISPLAY_NAME_MAX}
				autocomplete="off"
				placeholder="CleverTap"
				aria-invalid={err('/display_name') ? 'true' : undefined}
				aria-describedby={describedBy('/display_name')}
			/>
			<FieldError id="{fid('/display_name')}-error" message={err('/display_name')} />
		</div>
		<div class="fk-field">
			<label for={fid('/auth_scheme')}>Auth scheme <span class="fk-req">(required)</span></label>
			<select
				id={fid('/auth_scheme')}
				name="auth_scheme"
				bind:value={values.auth_scheme}
				aria-invalid={err('/auth_scheme') ? 'true' : undefined}
				aria-describedby={describedBy('/auth_scheme', true)}
			>
				{#each AUTH_SCHEMES as s (s)}<option value={s}>{authSchemeLabels[s]}</option>{/each}
			</select>
			<p id="{fid('/auth_scheme')}-hint" class="fk-hint">
				How accounts of this vendor authenticate. The secrets themselves go on each account.
			</p>
			<FieldError id="{fid('/auth_scheme')}-error" message={err('/auth_scheme')} />
		</div>
		<div class="fk-field">
			<label for={fid('/docs_url')}>API documentation URL</label>
			<input
				id={fid('/docs_url')}
				name="docs_url"
				type="url"
				bind:value={values.docs_url}
				onblur={leave('/docs_url')}
				autocomplete="off"
				placeholder="https://developer.example.com/docs"
				aria-invalid={err('/docs_url') ? 'true' : undefined}
				aria-describedby={describedBy('/docs_url')}
			/>
			<FieldError id="{fid('/docs_url')}-error" message={err('/docs_url')} />
		</div>
	</div>
</fieldset>

<fieldset class="fk-group" {disabled} aria-describedby="{fid('/regions')}-hint">
	<legend>Regions <span class="fk-req">(at least one)</span></legend>
	<p id="{fid('/regions')}-hint" class="fk-hint">
		Region code and base URL. These hosts are the only ones the Hub may call for this vendor (egress
		allowlist); each account picks one region.
	</p>
	<ul class="rows">
		{#each values.regions as r, i (i)}
			<li class="row">
				<div class="fk-field code">
					<label for="{id}-rc{i}">Region code</label>
					<input
						id="{id}-rc{i}"
						name={NAMES.regionCode}
						bind:value={r.code}
						onblur={leave('/regions')}
						maxlength="32"
						autocomplete="off"
						spellcheck="false"
						placeholder="in"
						aria-invalid={err('/regions') ? 'true' : undefined}
					/>
				</div>
				<div class="fk-field url">
					<label for="{id}-ru{i}">Base URL</label>
					<input
						id="{id}-ru{i}"
						name={NAMES.regionUrl}
						type="url"
						bind:value={r.url}
						onblur={leave('/regions')}
						autocomplete="off"
						spellcheck="false"
						placeholder="https://in1.api.example.com"
						aria-invalid={err('/regions') ? 'true' : undefined}
					/>
				</div>
				<button
					type="button"
					class="btn btn-quiet remove"
					onclick={() => removeRegion(i)}
					aria-label="Remove region {r.code || i + 1}"
				>
					<Icon name="close" size={14} />Remove
				</button>
			</li>
		{/each}
	</ul>
	<FieldError id="{fid('/regions')}-error" message={err('/regions')} />
	<div><button type="button" class="btn btn-quiet" onclick={addRegion}>Add region</button></div>
</fieldset>

<fieldset class="fk-group" {disabled}>
	<legend>Defaults</legend>
	<p class="fk-hint">
		Optional. Apply to every account and endpoint of this vendor unless an integration overrides
		them. Leave a field empty to use the Hub's default.
	</p>
	<div class="fk-grid">
		<div class="fk-field">
			<label for={fid('/defaults/rate_limit/per_account_rps')}>Rate limit per account (rps)</label>
			<input
				id={fid('/defaults/rate_limit/per_account_rps')}
				name="per_account_rps"
				inputmode="decimal"
				bind:value={values.per_account_rps}
				onblur={leave('/defaults/rate_limit/per_account_rps')}
				aria-invalid={err('/defaults/rate_limit/per_account_rps') ? 'true' : undefined}
				aria-describedby={describedBy('/defaults/rate_limit/per_account_rps')}
			/>
			<FieldError
				id="{fid('/defaults/rate_limit/per_account_rps')}-error"
				message={err('/defaults/rate_limit/per_account_rps')}
			/>
		</div>
	</div>
	<h3 class="sub">Circuit breaker</h3>
	<div class="fk-grid">
		{#each BREAKER_FIELDS as f (f.key)}
			{@const ptr = `/defaults/breaker/${f.key}`}
			<div class="fk-field">
				<label for={fid(ptr)}>{f.label}</label>
				<input
					id={fid(ptr)}
					name={NAMES.breaker(f.key)}
					inputmode={f.kind === 'ratio' ? 'decimal' : 'numeric'}
					bind:value={values.breaker[f.key]}
					onblur={leave(ptr)}
					aria-invalid={err(ptr) ? 'true' : undefined}
					aria-describedby={describedBy(ptr)}
				/>
				<FieldError id="{fid(ptr)}-error" message={err(ptr)} />
			</div>
		{/each}
	</div>
	<h3 class="sub">Health probe</h3>
	<div class="fk-grid">
		<div class="fk-field">
			<label for={fid('/defaults/health_probe/endpoint')}>Probe endpoint</label>
			<input
				id={fid('/defaults/health_probe/endpoint')}
				name="probe_endpoint"
				list="{id}-endpoint-refs"
				bind:value={values.probe_endpoint}
				onblur={leave('/defaults/health_probe/endpoint')}
				autocomplete="off"
				spellcheck="false"
				placeholder="upload@1"
				aria-invalid={err('/defaults/health_probe/endpoint') ? 'true' : undefined}
				aria-describedby={describedBy('/defaults/health_probe/endpoint', true)}
			/>
			<datalist id="{id}-endpoint-refs">
				{#each endpointRefs as ref (ref)}<option value={ref}></option>{/each}
			</datalist>
			<p id="{fid('/defaults/health_probe/endpoint')}-hint" class="fk-hint">
				Also what "Test connection" calls on an account.
			</p>
			<FieldError
				id="{fid('/defaults/health_probe/endpoint')}-error"
				message={err('/defaults/health_probe/endpoint')}
			/>
		</div>
		<div class="fk-field">
			<label for={fid('/defaults/health_probe/every_ms')}>Probe every (ms)</label>
			<input
				id={fid('/defaults/health_probe/every_ms')}
				name="probe_every_ms"
				inputmode="numeric"
				bind:value={values.probe_every_ms}
				onblur={leave('/defaults/health_probe/every_ms')}
				aria-invalid={err('/defaults/health_probe/every_ms') ? 'true' : undefined}
				aria-describedby={describedBy('/defaults/health_probe/every_ms')}
			/>
			<FieldError
				id="{fid('/defaults/health_probe/every_ms')}-error"
				message={err('/defaults/health_probe/every_ms')}
			/>
		</div>
		<div class="fk-field">
			<span class="fk-label">Probe mode</span>
			<label class="fk-check">
				<input type="checkbox" name="probe_dry" bind:checked={values.probe_dry} />
				Dry probe (no side effects at the vendor)
			</label>
		</div>
	</div>
</fieldset>

<fieldset class="fk-group" {disabled}>
	<legend>Inbound webhook verification</legend>
	<div class="fk-grid">
		<div class="fk-field">
			<label for={fid('/webhook_verification/scheme')}>Scheme</label>
			<select
				id={fid('/webhook_verification/scheme')}
				name="webhook_scheme"
				bind:value={values.webhook_scheme}
				aria-invalid={err('/webhook_verification/scheme') ? 'true' : undefined}
				aria-describedby={describedBy('/webhook_verification/scheme', true)}
			>
				<option value="">Not set (vendor sends no webhooks)</option>
				{#each WEBHOOK_SCHEMES as s (s)}<option value={s}>{webhookSchemeLabels[s]}</option>{/each}
			</select>
			<p id="{fid('/webhook_verification/scheme')}-hint" class="fk-hint">
				How the Hub checks webhooks this vendor sends back.
			</p>
			<FieldError
				id="{fid('/webhook_verification/scheme')}-error"
				message={err('/webhook_verification/scheme') ?? err('/webhook_verification')}
			/>
		</div>
		{#if values.webhook_scheme && values.webhook_scheme !== 'none'}
			<div class="fk-field">
				<label for={fid('/webhook_verification/header')}>Signature header</label>
				<input
					id={fid('/webhook_verification/header')}
					name="webhook_header"
					bind:value={values.webhook_header}
					onblur={leave('/webhook_verification/header')}
					autocomplete="off"
					spellcheck="false"
					placeholder="X-Vendor-Signature"
					aria-invalid={err('/webhook_verification/header') ? 'true' : undefined}
					aria-describedby={describedBy('/webhook_verification/header')}
				/>
				<FieldError
					id="{fid('/webhook_verification/header')}-error"
					message={err('/webhook_verification/header')}
				/>
			</div>
			<div class="fk-field">
				<label for={fid('/webhook_verification/max_skew_ms')}>Maximum clock skew (ms)</label>
				<input
					id={fid('/webhook_verification/max_skew_ms')}
					name="webhook_max_skew_ms"
					inputmode="numeric"
					bind:value={values.webhook_max_skew_ms}
					onblur={leave('/webhook_verification/max_skew_ms')}
					aria-invalid={err('/webhook_verification/max_skew_ms') ? 'true' : undefined}
					aria-describedby={describedBy('/webhook_verification/max_skew_ms')}
				/>
				<FieldError
					id="{fid('/webhook_verification/max_skew_ms')}-error"
					message={err('/webhook_verification/max_skew_ms')}
				/>
			</div>
			<div class="fk-field wide">
				<label for={fid('/webhook_verification/ip_allowlist')}>Source IP allowlist</label>
				<textarea
					id={fid('/webhook_verification/ip_allowlist')}
					name="webhook_ip_allowlist"
					class="fk-mono"
					rows="3"
					bind:value={values.webhook_ip_allowlist}
					onblur={leave('/webhook_verification/ip_allowlist')}
					spellcheck="false"
					placeholder="203.0.113.0/24"
					aria-invalid={err('/webhook_verification/ip_allowlist') ? 'true' : undefined}
					aria-describedby={describedBy('/webhook_verification/ip_allowlist', true)}></textarea>
				<p id="{fid('/webhook_verification/ip_allowlist')}-hint" class="fk-hint">
					One address or CIDR range per line.
				</p>
				<FieldError
					id="{fid('/webhook_verification/ip_allowlist')}-error"
					message={err('/webhook_verification/ip_allowlist')}
				/>
			</div>
		{/if}
	</div>
</fieldset>

<style>
	.rows {
		display: grid;
		gap: var(--space-2);
		margin: 0;
		padding: 0;
		list-style: none;
	}
	.row {
		display: grid;
		grid-template-columns: minmax(6rem, 10rem) minmax(0, 1fr) auto;
		gap: var(--space-2);
		align-items: end;
	}
	@media (max-width: 640px) {
		.row {
			grid-template-columns: minmax(0, 1fr) auto;
		}
		.row .url {
			grid-column: 1 / -1;
			grid-row: 2;
		}
	}
	.remove {
		margin-bottom: 0.2rem;
	}
	.sub {
		margin: var(--space-2) 0 0;
		font-size: var(--text-sm);
		font-weight: 700;
	}
</style>

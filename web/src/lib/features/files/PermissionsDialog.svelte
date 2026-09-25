<script lang="ts" module>
	export interface PermissionChange {
		recursive: boolean;
		chmod?: { mode: string; dirMode?: string };
		chown?: { uid: number; gid: number };
	}
</script>

<script lang="ts">
	// chmod/chown of a selection (#15): mode as read/write/execute checkboxes
	// synced with the octal value (host semantics, special bits cannot be
	// set), numeric owner and group, optional recursion with an impact
	// preview (symlinks are never followed). Per-item errors come back in
	// the job's items.
	import {
		Button,
		Checkbox,
		Dialog,
		Switch,
		TextField,
		errorMessage,
		formatBytes
	} from '$lib/ui';
	import type { FileEntry, FilePreview } from './api';
	import { modeString } from './icons';

	interface Props {
		open?: boolean;
		entries: FileEntry[];
		canChmod: boolean;
		canChown: boolean;
		preview: (recursive: boolean) => Promise<FilePreview>;
		onsubmit: (change: PermissionChange) => Promise<unknown>;
	}

	let {
		open = $bindable(false),
		entries,
		canChmod,
		canChown,
		preview,
		onsubmit
	}: Props = $props();

	const WHO = [
		{ id: 'owner', label: 'Owner', shift: 6 },
		{ id: 'group', label: 'Group', shift: 3 },
		{ id: 'other', label: 'Others', shift: 0 }
	] as const;
	const BITS = [
		{ id: 'r', label: 'Read', bit: 4 },
		{ id: 'w', label: 'Write', bit: 2 },
		{ id: 'x', label: 'Execute', bit: 1 }
	] as const;

	const hasDirs = $derived(entries.some((e) => e.type === 'dir'));
	const hasFiles = $derived(entries.some((e) => e.type !== 'dir'));

	let changeMode = $state(true);
	let changeOwner = $state(false);
	let bits = $state(0o644);
	let octal = $state('0644');
	let separateDirs = $state(false);
	let dirOctal = $state('0755');
	let uid = $state('0');
	let gid = $state('0');
	let recursive = $state(false);
	let impact = $state<FilePreview['impact'] | null>(null);
	let impactError = $state<string | null>(null);
	let error = $state<string | null>(null);
	let busy = $state(false);

	$effect(() => {
		if (!open) return;
		const first = entries[0];
		const m = first ? parseInt(first.mode.slice(-3), 8) : 0o644;
		bits = Number.isNaN(m) ? 0o644 : m;
		octal = '0' + bits.toString(8).padStart(3, '0');
		changeMode = canChmod;
		changeOwner = !canChmod && canChown;
		separateDirs = false;
		dirOctal = '0755';
		uid = String(first?.uid ?? 0);
		gid = String(first?.gid ?? 0);
		recursive = false;
		error = null;
	});

	// Impact of the change (recursive counts everything below folders).
	$effect(() => {
		if (!open) return;
		const r = recursive;
		impact = null;
		impactError = null;
		let stale = false;
		preview(r)
			.then((p) => !stale && (impact = p.impact))
			.catch((e) => !stale && (impactError = errorMessage(e)));
		return () => (stale = true);
	});

	function setBit(who: number, bit: number, on: boolean) {
		const mask = bit << who;
		bits = on ? bits | mask : bits & ~mask;
		octal = '0' + bits.toString(8).padStart(3, '0');
	}

	function onOctal(v: string) {
		octal = v;
		if (/^0?[0-7]{3}$/.test(v)) bits = parseInt(v.slice(-3), 8);
	}

	const octalValid = $derived(/^0?[0-7]{3}$/.test(octal));
	const dirValid = $derived(!separateDirs || /^0?[0-7]{3}$/.test(dirOctal));
	const idValid = $derived(
		!changeOwner ||
			(/^\d{1,10}$/.test(uid) && /^\d{1,10}$/.test(gid) && +uid < 2 ** 31 && +gid < 2 ** 31)
	);
	const valid = $derived(
		(changeMode || changeOwner) && (!changeMode || (octalValid && dirValid)) && idValid
	);

	function impactText(): string {
		if (!impact) return '';
		const parts: string[] = [];
		if (impact.dirs) parts.push(`${impact.dirs} ${impact.dirs === 1 ? 'folder' : 'folders'}`);
		if (impact.files) parts.push(`${impact.files} ${impact.files === 1 ? 'file' : 'files'}`);
		if (impact.symlinks) parts.push(`${impact.symlinks} links (left unchanged)`);
		const what = parts.join(', ') || 'nothing';
		return `Changes ${what}${impact.truncated ? ' or more' : ''} (${formatBytes(impact.bytes)}).`;
	}

	async function submit(e: Event) {
		e.preventDefault();
		if (!valid) return;
		busy = true;
		error = null;
		try {
			const pad = (s: string) => (s.length === 3 ? '0' + s : s);
			await onsubmit({
				recursive,
				chmod: changeMode
					? {
							mode: pad(octal),
							dirMode: separateDirs && hasDirs ? pad(dirOctal) : undefined
						}
					: undefined,
				chown: changeOwner ? { uid: +uid, gid: +gid } : undefined
			});
			open = false;
		} catch (err) {
			error = errorMessage(err);
		} finally {
			busy = false;
		}
	}
</script>

<Dialog
	bind:open
	title="Change permissions"
	description="{entries.length === 1
		? entries[0].name
		: `${entries.length} items`}: modes and owners use the host's numeric IDs."
	size="md"
	dismissible={!busy}
>
	<form id="permissions-form" class="form" onsubmit={submit}>
		{#if canChmod}
			<fieldset>
				<legend>
					<Switch bind:checked={changeMode} label="Change mode" />
				</legend>
				{#if changeMode}
					<table class="matrix">
						<thead>
							<tr>
								<th scope="col"><span class="sr-only">Who</span></th>
								{#each BITS as b (b.id)}<th scope="col">{b.label}</th>{/each}
							</tr>
						</thead>
						<tbody>
							{#each WHO as w (w.id)}
								<tr>
									<th scope="row">{w.label}</th>
									{#each BITS as b (b.id)}
										<td>
											<Checkbox
												label="{w.label} {b.label.toLowerCase()}"
												hideLabel
												checked={(bits & (b.bit << w.shift)) !== 0}
												onchange={(e) =>
													setBit(w.shift, b.bit, e.currentTarget.checked)}
											/>
										</td>
									{/each}
								</tr>
							{/each}
						</tbody>
					</table>
					<div class="row">
						<TextField
							label={hasDirs && separateDirs ? 'Mode for files' : 'Mode'}
							mono
							value={octal}
							oninput={(e) => onOctal(e.currentTarget.value)}
							error={octalValid ? null : 'Use three octal digits, e.g. 0644.'}
							description={octalValid ? modeString(octal) : undefined}
						/>
						{#if hasDirs}
							<div class="dirs">
								<Switch
									bind:checked={separateDirs}
									label="Use another mode for folders"
									description={hasFiles || recursive
										? 'Folders usually need execute to be opened.'
										: undefined}
								/>
								{#if separateDirs}
									<TextField
										label="Mode for folders"
										mono
										bind:value={dirOctal}
										error={dirValid
											? null
											: 'Use three octal digits, e.g. 0755.'}
									/>
								{/if}
							</div>
						{/if}
					</div>
				{/if}
			</fieldset>
		{/if}
		{#if canChown}
			<fieldset>
				<legend><Switch bind:checked={changeOwner} label="Change owner" /></legend>
				{#if changeOwner}
					<div class="row">
						<TextField
							label="Owner ID (UID)"
							mono
							inputmode="numeric"
							bind:value={uid}
						/>
						<TextField
							label="Group ID (GID)"
							mono
							inputmode="numeric"
							bind:value={gid}
						/>
					</div>
					{#if !idValid}<p class="field-error">
							Use numeric IDs from 0 to 2147483647.
						</p>{/if}
				{/if}
			</fieldset>
		{/if}
		{#if hasDirs}
			<Switch
				bind:checked={recursive}
				label="Apply to everything inside the selected folders"
				description="Symbolic links are never followed."
			/>
		{/if}
		<p class="impact" aria-live="polite">
			{#if impactError}{impactError}{:else if impact}{impactText()}{:else}Counting what
				changes…{/if}
			Setuid, setgid and sticky bits can't be set.
		</p>
		{#if error}<p class="field-error" role="alert">{error}</p>{/if}
	</form>
	{#snippet footer()}
		<Button variant="ghost" onclick={() => (open = false)} disabled={busy}>Cancel</Button>
		<Button
			variant="primary"
			type="submit"
			form="permissions-form"
			loading={busy}
			disabled={!valid}>Change permissions</Button
		>
	{/snippet}
</Dialog>

<style>
	.form {
		display: grid;
		gap: var(--space-4);
	}

	fieldset {
		display: grid;
		gap: var(--space-3);
		margin: 0;
		padding: 0;
		border: 0;
	}

	legend {
		margin-bottom: var(--space-2);
		padding: 0;
	}

	.matrix {
		border-collapse: collapse;
		font-size: var(--text-body);
	}

	.matrix th,
	.matrix td {
		padding: 4px var(--space-3);
		text-align: center;
	}

	.matrix th {
		color: var(--text-muted);
		font-weight: var(--weight-regular);
	}

	.matrix th[scope='row'] {
		text-align: left;
		color: var(--text-default);
	}

	.row {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
		gap: var(--space-3);
	}

	.dirs {
		display: grid;
		gap: var(--space-2);
	}

	.impact {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.field-error {
		color: var(--danger);
		font-size: var(--text-caption);
	}
</style>

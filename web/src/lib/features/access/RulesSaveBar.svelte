<script lang="ts">
	// Save bar of a permission editor (#17): how many rules changed, Discard,
	// and Save with a confirmation that lists every change. Saving is
	// revisioned (If-Match), audited with the diff and needs a step-up; the
	// affected users' open requests and streams end at once.
	import { Button, ConfirmDialog } from '$lib/ui';
	import {
		capabilityLabel,
		diffRules,
		scopeLabel,
		type Catalog,
		type Rule,
		type Scope
	} from './permissions';

	interface Props {
		before: Rule[];
		after: Rule[];
		catalog?: Catalog;
		subject: string;
		mode: 'group' | 'user';
		environmentName?: (id: string) => string;
		/** A resource's name where the page knows it (the tree's lists, #281). */
		resourceName?: () => (s: Scope) => string | undefined;
		ondiscard: () => void;
		onsave: () => Promise<unknown>;
	}

	let {
		before,
		after,
		catalog,
		subject,
		mode,
		environmentName,
		resourceName,
		ondiscard,
		onsave
	}: Props = $props();
	let open = $state(false);

	const changes = $derived(diffRules(before, after));
	const word = (e: 'allow' | 'deny' | null) =>
		e === 'allow' ? 'Allow' : e === 'deny' ? 'Deny' : mode === 'user' ? 'Inherit' : 'No Rule';
	// Resources by name where the tree listed them (read when the list is shown).
	const lines = $derived.by(() => {
		const resource = open ? resourceName?.() : undefined;
		return changes.map(
			(c) =>
				`${capabilityLabel(catalog, c.capability)} on ${scopeLabel(c.scope, { environment: environmentName, resource })}: ${word(c.before)} → ${word(c.after)}`
		);
	});
</script>

{#if changes.length}
	<div class="bar" role="region" aria-label="Unsaved Permission Changes">
		<p>
			<strong class="num">{changes.length}</strong> unsaved {changes.length === 1
				? 'change'
				: 'changes'}
		</p>
		<div class="buttons">
			<Button variant="ghost" onclick={ondiscard}>Discard</Button>
			<Button variant="primary" onclick={() => (open = true)}>Save Permissions</Button>
		</div>
	</div>
{/if}

<ConfirmDialog
	bind:open
	title="Save the permissions of {subject}?"
	message={mode === 'group'
		? 'Every member gets these rules at once; their open pages and streams restart.'
		: 'These overrides apply at once; the user’s open pages and streams restart.'}
	consequences={lines}
	confirmLabel="Save Permissions"
	onconfirm={onsave}
/>

<style>
	.bar {
		position: sticky;
		bottom: var(--space-4);
		z-index: var(--z-sticky);
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-3);
		padding: var(--space-3) var(--space-4);
		border: 1px solid var(--border-strong);
		border-radius: var(--radius-lg);
		background: var(--surface-raised);
		box-shadow: var(--shadow-float);
	}

	.buttons {
		display: flex;
		gap: var(--space-2);
	}

	strong {
		color: var(--text-strong);
	}
</style>

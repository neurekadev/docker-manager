<script lang="ts">
	// Start, Restart and Stop of a stack or container as one split button
	// (#22). The main part is Stop (danger-soft, Square) while anything runs,
	// a partially running stack included, and Start (ok-soft, Play) while
	// nothing does; the menu lists Start, Restart and Stop (secondary,
	// RotateCw for Restart) as the caller holds them, the ones that do not
	// apply in the state turned off (`lifecycle.ts`). With a single held
	// action it is a plain button. The callbacks decide what runs at once
	// (Start, Restart) and what confirms first (Stop). While an action runs
	// (`busy`) the main part shows it with a spinner.
	import Play from '@lucide/svelte/icons/play';
	import RotateCw from '@lucide/svelte/icons/rotate-cw';
	import Square from '@lucide/svelte/icons/square';
	import type { IconComponent } from '$lib/design/icons';
	import {
		Button,
		SplitButton,
		type ButtonSize,
		type MenuEntry,
		type SplitButtonVariant
	} from '$lib/ui';
	import {
		LIFECYCLE_LABELS,
		lifecycleEntries,
		lifecycleMain,
		type LifecycleActions,
		type LifecycleVerb
	} from './lifecycle';

	interface Props {
		/** Anything runs (Stop is the main action); else Start is. */
		running: boolean;
		actions: LifecycleActions;
		/** The action that is being started (spinner on the main part). */
		busy?: LifecycleVerb | null;
		/** Turns everything off (offline, another operation on the object). */
		disabled?: boolean;
		/** Why everything is off (the main part's tooltip). */
		reason?: string;
		size?: ButtonSize;
		menuLabel?: string;
	}

	let {
		running,
		actions,
		busy = null,
		disabled = false,
		reason,
		size = 'md',
		menuLabel = 'More start and stop options'
	}: Props = $props();

	const LOOK: Record<LifecycleVerb, { icon: IconComponent; variant: SplitButtonVariant }> = {
		start: { icon: Play, variant: 'ok-soft' },
		restart: { icon: RotateCw, variant: 'secondary' },
		stop: { icon: Square, variant: 'danger-soft' }
	};

	const main = $derived(busy ?? lifecycleMain(running, actions));
	const action = $derived(main ? actions[main] : undefined);
	const mainOff = $derived(disabled || !!action?.disabled || busy !== null);
	const mainTitle = $derived(disabled ? reason : action?.disabled ? action.reason : undefined);
	const entries = $derived(lifecycleEntries(actions, disabled || busy !== null));
	const items = $derived(
		entries.map((e): MenuEntry => ({
			label: e.label,
			icon: LOOK[e.verb].icon,
			tone: e.verb === 'stop' ? 'danger' : undefined,
			disabled: e.disabled,
			onSelect: () => actions[e.verb]?.run()
		}))
	);
</script>

{#if main}
	{#if items.length > 1}
		<SplitButton
			label={LIFECYCLE_LABELS[main]}
			icon={LOOK[main].icon}
			variant={LOOK[main].variant}
			{size}
			{menuLabel}
			{items}
			loading={busy !== null}
			disabled={mainOff}
			menuDisabled={disabled}
			title={mainTitle}
			onclick={() => action?.run()}
		/>
	{:else}
		<Button
			icon={LOOK[main].icon}
			variant={LOOK[main].variant}
			{size}
			loading={busy !== null}
			disabled={mainOff}
			title={mainTitle}
			onclick={() => action?.run()}>{LIFECYCLE_LABELS[main]}</Button
		>
	{/if}
{/if}

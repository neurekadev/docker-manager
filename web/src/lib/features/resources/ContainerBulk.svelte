<script lang="ts">
	// Bulk actions on the selected containers (#6, #32, #22 polish): start,
	// stop, restart and remove, each confirmed with the list of containers
	// it runs on and those it leaves out with the reason (Docker Manager's
	// own containers, containers of a managed stack), then sent one by one
	// through the single-container requests with one summary toast.
	import { useQueryClient } from '@tanstack/svelte-query';
	import Play from '@lucide/svelte/icons/play';
	import RotateCw from '@lucide/svelte/icons/rotate-cw';
	import Square from '@lucide/svelte/icons/square';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import { queryKeys, type Container } from '$lib/api/queries';
	import BulkBar, { type BulkAction } from './BulkBar.svelte';
	import BulkConfirm from './BulkConfirm.svelte';
	import { planContainers, type BulkPlan, type ContainerBulkVerb } from './bulk';
	import { runBulk } from './bulk-run';
	import { containerActions, runContainerAction } from './container-actions';

	interface Props {
		selected: Container[];
		environmentName: (env: string) => string | undefined;
		onclear: () => void;
	}

	let { selected, environmentName, onclear }: Props = $props();
	const queryClient = useQueryClient();

	let verb = $state<ContainerBulkVerb>('stop');
	let open = $state(false);
	let plan = $state<BulkPlan<Container>>({ run: [], refused: [], skipped: [] });

	const offered = (v: ContainerBulkVerb) =>
		selected.some((c) => containerActions(c).some((a) => a.verb === v));

	function ask(v: ContainerBulkVerb) {
		verb = v;
		plan = planContainers(selected, v);
		open = true;
	}

	const LABEL: Record<ContainerBulkVerb, string> = {
		start: 'Start',
		stop: 'Stop',
		restart: 'Restart',
		remove: 'Remove'
	};
	const ICON = { start: Play, stop: Square, restart: RotateCw, remove: Trash2 };
	const actions = $derived<BulkAction[]>(
		(['start', 'stop', 'restart', 'remove'] as const).filter(offered).map((v) => ({
			label: LABEL[v],
			icon: ICON[v],
			danger: v === 'remove',
			onclick: () => ask(v)
		}))
	);

	const CONSEQUENCES: Record<ContainerBulkVerb, string[]> = {
		start: [],
		stop: [
			'Each container gets a stop signal and is killed if it does not exit in time.',
			'Their restart policies do not start them again.'
		],
		restart: ['Each container is stopped and started again.'],
		remove: ['Running containers are stopped first.', 'Named volumes and their data are kept.']
	};

	const n = $derived(plan.run.length);
	const noun = $derived(n === 1 ? 'container' : 'containers');
	const Noun = $derived(n === 1 ? 'Container' : 'Containers');

	async function confirm() {
		const v = verb;
		await runBulk({
			plan,
			verb: v,
			noun: { one: 'container', many: 'containers' },
			name: (c) => c.name,
			send: (c) =>
				runContainerAction(
					c.environmentId,
					c.name,
					v,
					v === 'remove'
						? { force: c.state === 'running' || c.state === 'restarting' }
						: {}
				),
			ctx: (c) => ({
				kind: 'container',
				name: c.name,
				verb: v,
				protection: c.protection,
				environmentName: environmentName(c.environmentId)
			}),
			queryClient,
			invalidate: [queryKeys.containers.all, ['stacks', 'services']]
		});
		onclear();
	}
</script>

{#if selected.length}
	<BulkBar count={selected.length} noun="containers" {actions} {onclear} />
{/if}

<BulkConfirm
	bind:open
	title="{LABEL[verb]} {n} {noun}?"
	consequences={CONSEQUENCES[verb]}
	{plan}
	name={(c) => c.name}
	confirmLabel="{LABEL[verb]} {n} {Noun}"
	danger={verb === 'stop' || verb === 'remove'}
	confirmText={verb === 'remove' ? `remove ${n} ${noun}` : undefined}
	onconfirm={confirm}
/>

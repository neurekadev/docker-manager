<script lang="ts" module>
	import type { Image, Network, Volume } from '$lib/api/queries';

	export type BulkObject =
		| { kind: 'image'; items: Image[] }
		| { kind: 'volume'; items: Volume[] }
		| { kind: 'network'; items: Network[]; attached?: ReadonlySet<string> };
</script>

<script lang="ts">
	// Bulk removal of the selected images, volumes or networks (#6, #32,
	// #22 polish): confirmed with the list of what is removed and what is
	// left out with the reason (Docker Manager's own, in use, predefined),
	// confirmed by typing, then sent one by one through the single-object
	// requests with one summary toast.
	import { useQueryClient } from '@tanstack/svelte-query';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import { queryKeys } from '$lib/api/queries';
	import BulkBar from './BulkBar.svelte';
	import BulkConfirm from './BulkConfirm.svelte';
	import { planImageRemoval, planNetworkRemoval, planVolumeRemoval, type BulkPlan } from './bulk';
	import { runBulk } from './bulk-run';
	import { shortDigest } from './model';
	import { removeImage, removeNetwork, removeVolume } from './object-actions';

	interface Props {
		selected: BulkObject;
		environmentName: (env: string) => string | undefined;
		onclear: () => void;
	}

	let { selected, environmentName, onclear }: Props = $props();
	const queryClient = useQueryClient();

	type Item = Image | Volume | Network;
	let open = $state(false);
	let plan = $state<BulkPlan<Item>>({ run: [], refused: [], skipped: [] });

	const NOUN = {
		image: { one: 'image', many: 'images' },
		volume: { one: 'volume', many: 'volumes' },
		network: { one: 'network', many: 'networks' }
	};
	/** The nouns in a button label (Title Case). */
	const LABEL_NOUN = {
		image: { one: 'Image', many: 'Images' },
		volume: { one: 'Volume', many: 'Volumes' },
		network: { one: 'Network', many: 'Networks' }
	};
	const kind = $derived(selected.kind);
	const n = $derived(plan.run.length);
	const noun = $derived(n === 1 ? NOUN[kind].one : NOUN[kind].many);
	const Noun = $derived(n === 1 ? LABEL_NOUN[kind].one : LABEL_NOUN[kind].many);

	/** Images are named by their first tag (untagged: the short ID). */
	function name(item: Item): string {
		if ('repoTags' in item) return item.repoTags[0] ?? shortDigest(item.id);
		return item.name;
	}

	function makePlan(): BulkPlan<Item> {
		const s = selected;
		if (s.kind === 'image') return planImageRemoval(s.items);
		if (s.kind === 'volume') return planVolumeRemoval(s.items);
		return planNetworkRemoval(s.items, s.attached);
	}

	function ask() {
		plan = makePlan();
		open = true;
	}

	const CONSEQUENCES = {
		image: [
			'The images are deleted from their environments; pull them again when you need them.'
		],
		volume: ['Each volume and every file in it are deleted. This cannot be undone.'],
		network: ['The networks are deleted from their environments.']
	};

	function send(item: Item) {
		if ('repoTags' in item)
			return removeImage(item.environmentId, item.id, { force: item.repoTags.length > 1 });
		return kind === 'volume'
			? removeVolume(item.environmentId, item.name)
			: removeNetwork(item.environmentId, item.name);
	}

	async function confirm() {
		const k = kind;
		await runBulk<Item>({
			plan,
			verb: 'remove',
			noun: NOUN[k],
			name,
			send,
			ctx: (item) => ({
				kind: k,
				name: name(item),
				verb: 'remove',
				protection: item.protection,
				environmentName: environmentName(item.environmentId)
			}),
			queryClient,
			invalidate: [
				k === 'image'
					? queryKeys.images.all
					: k === 'volume'
						? queryKeys.volumes.all
						: queryKeys.networks.all
			]
		});
		onclear();
	}
</script>

{#if selected.items.length}
	<BulkBar
		count={selected.items.length}
		noun={NOUN[kind].many}
		actions={[{ label: 'Remove', icon: Trash2, danger: true, onclick: ask }]}
		{onclear}
	/>
{/if}

<BulkConfirm
	bind:open
	title="Remove {n} {noun}?"
	consequences={CONSEQUENCES[kind]}
	{plan}
	{name}
	confirmLabel={kind === 'volume' ? `Remove ${n} ${Noun} and Their Data` : `Remove ${n} ${Noun}`}
	danger
	confirmText="remove {n} {noun}"
	onconfirm={confirm}
/>

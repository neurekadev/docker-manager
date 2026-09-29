<script lang="ts">
	// The findings of an environment migration's check (#35), shared by the
	// environment migration wizard and the manager move's Check step: the
	// problems of the whole migration and of each stack, data against free
	// space, the longest downtime, the order (stacks that share a network or
	// volume form a group that stops together and moves one after the
	// other), networks created first, what is not moved and why, warnings
	// and each stack's details. The headline and "Check again" stay with
	// the caller.
	import Disclosure from '$lib/features/common/Disclosure.svelte';
	import MigrationFindings from '$lib/features/stacks/MigrationFindings.svelte';
	import {
		accessChangeText,
		imageActionLabel,
		sentence,
		volumeActionLabel
	} from '$lib/features/stacks/migration';
	import { downtimeText, spaceCheck } from '$lib/features/stacks/model';
	import { Badge, formatBytes, shortId } from '$lib/ui';
	import {
		andList,
		moveOrder,
		problemCount,
		skippedRows,
		stackMoveSummary,
		type EnvironmentMigrationPreview,
		type TitleOf
	} from './environment-migration';

	interface Props {
		preview: EnvironmentMigrationPreview;
		/** The source environment's name. */
		sourceName: string;
		/** The destination environment's name. */
		destName: string;
		/** Resolves a stack's ID to its title. */
		titleOf: TitleOf;
		/** Docker Manager's own stacks (they keep their reason when not selected). */
		own: readonly string[];
	}

	let { preview, sourceName, destName, titleOf, own }: Props = $props();

	const space = $derived(spaceCheck(preview.data));
	const blocked = $derived(preview.stacks.filter((x) => x.preview.blockers.length > 0));
	const skipped = $derived(skippedRows(preview, own, titleOf));

	/** A moving stack's title by ID (networks name the stacks that join them). */
	const moveTitle = (id: string) => {
		const s = preview.stacks.find((x) => x.stackId === id);
		return s ? titleOf(id, s.name) : 'a stack';
	};
</script>

<div class="findings">
	{#if problemCount(preview)}
		<section aria-labelledby="blockers-title">
			<h3 id="blockers-title" class="subsection-title">To fix before moving</h3>
			<div class="blockers">
				{#if preview.blockers.length}
					<MigrationFindings list={preview.blockers} tone="danger" />
				{/if}
				{#each blocked as b (b.stackId)}
					<div>
						<h4 class="stack-name">{titleOf(b.stackId, b.name)}</h4>
						<MigrationFindings list={b.preview.blockers} tone="danger" />
					</div>
				{/each}
			</div>
		</section>
	{/if}

	<dl class="facts">
		<div>
			<dt>Data to copy</dt>
			<dd class="num">
				{formatBytes(preview.data.totalBytes)}{preview.data.truncated ? ' or more' : ''}
			</dd>
		</div>
		<div>
			<dt>Free on {destName}</dt>
			<dd class="num">
				{#if space === 'unknown'}Unknown{:else}
					{formatBytes(
						Math.min(
							preview.data.destinationStacksFree,
							preview.data.destinationVolumesFree
						)
					)}
					{#if space === 'short'}<Badge tone="danger" dot>Not enough</Badge>{:else}<Badge
							tone="ok"
							dot>Enough</Badge
						>{/if}
				{/if}
			</dd>
		</div>
		<div>
			<dt>Longest downtime</dt>
			<dd>{downtimeText(preview.downtime.estimatedSeconds)}</dd>
			<dd class="basis">{sentence(preview.downtime.basis)}</dd>
		</div>
	</dl>

	{#if preview.groups.length}
		<section aria-labelledby="order-title">
			<h3 id="order-title" class="subsection-title">Order</h3>
			<ol class="groups" role="list">
				{#each moveOrder(preview, titleOf) as g (g.key)}
					<li>
						{#if g.stacks.length === 1}
							<span class="strong">{g.stacks[0].title}</span> moves on its own.
						{:else}
							<p>
								These share a network or volume: they stop together and move in this
								order.
							</p>
							<ol class="plain">
								{#each g.stacks as st (st.stackId)}
									<li>
										<span class="strong">{st.title}</span>
										{#if st.after.length}<span class="muted">
												· after {andList(st.after)}</span
											>{/if}
									</li>
								{/each}
							</ol>
						{/if}
					</li>
				{/each}
			</ol>
		</section>
	{/if}

	{#if preview.networks.length}
		<section aria-labelledby="networks-title">
			<h3 id="networks-title" class="subsection-title">Created first on {destName}</h3>
			<ul class="plain" role="list">
				{#each preview.networks as n (n.name)}
					<li>
						<span class="strong mono">{n.name}</span>
						<span class="muted">
							· network used by {andList(n.usedBy.map(moveTitle))}</span
						>
					</li>
				{/each}
			</ul>
		</section>
	{/if}

	<section aria-labelledby="skipped-title">
		<h3 id="skipped-title" class="subsection-title">Not moved</h3>
		{#if skipped.length}
			<ul class="plain" role="list">
				{#each skipped as r (r.stackId)}
					<li><span class="strong">{r.title}</span>: {r.reason}</li>
				{/each}
			</ul>
		{/if}
		<p class="muted hint">
			Containers outside stacks and volumes no stack uses stay on {sourceName}.
		</p>
	</section>

	{#if preview.warnings.length}
		<section aria-labelledby="warnings-title">
			<h3 id="warnings-title" class="subsection-title">Warnings</h3>
			<MigrationFindings list={preview.warnings} tone="warn" />
		</section>
	{/if}

	{#if preview.stacks.length}
		<section aria-labelledby="stacks-title">
			<h3 id="stacks-title" class="subsection-title">Stacks</h3>
			<div class="details">
				{#each preview.stacks as m (m.stackId)}
					<Disclosure summary="{titleOf(m.stackId, m.name)}: {stackMoveSummary(m)}">
						<div class="detail">
							{#if m.preview.volumes.length}
								<div>
									<h4 class="stack-name">Volumes</h4>
									<ul class="plain" role="list">
										{#each m.preview.volumes as v (v.source)}
											<li>
												{#if v.anonymous}Anonymous volume
													<span class="mono muted"
														>{shortId(v.source)}</span
													>{:else}<span class="strong"
														>{v.key ?? v.source}</span
													>{/if}
												<span class="muted">
													· {volumeActionLabel(v.action)}{v.action ===
													'copy'
														? `, ${formatBytes(v.bytes)}${v.truncated ? '+' : ''}`
														: ''}</span
												>
											</li>
										{/each}
									</ul>
								</div>
							{/if}
							{#if m.preview.services.length}
								<div>
									<h4 class="stack-name">Images</h4>
									<ul class="plain" role="list">
										{#each m.preview.services as svc (svc.name)}
											<li>
												<span class="strong">{svc.name}</span>
												<span class="mono">{svc.image}</span>
												<span class="muted">
													· {imageActionLabel(svc.action)}</span
												>
											</li>
										{/each}
									</ul>
								</div>
							{/if}
							{#if m.preview.warnings.length}
								<div>
									<h4 class="stack-name">Warnings</h4>
									<MigrationFindings list={m.preview.warnings} tone="warn" />
								</div>
							{/if}
							{#if m.preview.access.changes.length}
								<div>
									<h4 class="stack-name">Access changes</h4>
									<ul class="plain" role="list">
										{#each m.preview.access.changes as c (c.userId)}
											<li>
												<span class="strong">{c.username}</span>
												{accessChangeText(c)}
											</li>
										{/each}
									</ul>
								</div>
							{/if}
						</div>
					</Disclosure>
				{/each}
			</div>
		</section>
	{/if}
</div>

<style>
	.findings {
		display: grid;
		gap: var(--space-5);
	}

	.subsection-title {
		margin-bottom: var(--space-2);
	}

	.blockers,
	.details,
	.detail {
		display: grid;
		gap: var(--space-3);
	}

	.stack-name {
		margin: 0 0 var(--space-1);
		color: var(--text-strong);
		font-size: var(--text-body);
		font-weight: var(--weight-medium);
	}

	.facts {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
		gap: var(--space-3);
		margin: 0;
	}

	.facts > div {
		padding: var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-md);
		background: var(--surface-raised);
	}

	dt {
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	dd {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
		margin: 2px 0 0;
		color: var(--text-strong);
	}

	.basis {
		margin-top: var(--space-1);
		color: var(--text-muted);
		font-size: var(--text-caption);
	}

	.groups {
		display: grid;
		gap: var(--space-2);
		margin: 0;
		padding: 0;
		list-style: none;
	}

	.groups > li {
		padding: var(--space-2) var(--space-3);
		border: 1px solid var(--border-subtle);
		border-radius: var(--radius-sm);
		background: var(--surface-raised);
	}

	.groups > li > p {
		margin-bottom: var(--space-2);
	}

	.plain {
		display: grid;
		gap: var(--space-2);
		margin: 0;
		padding-left: 18px;
	}

	.hint {
		margin-top: var(--space-2);
	}

	.strong {
		color: var(--text-strong);
	}
</style>

<script lang="ts">
	// Create an API token (#31): a name, an expiry within the instance
	// maximum, and grants chosen in the permission tree from what I hold
	// now. The token's access is always its grants intersected with my
	// current permissions. The value is shown once (step-up required).
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { goto } from '$app/navigation';
	import { api, unwrap, type Schema } from '$lib/api/client';
	import { myPermissionsQuery } from '$lib/api/queries';
	import { withStepUp } from '$lib/auth/stepup.svelte';
	import { routes } from '$lib/routes';
	import { accessOf } from '$lib/shell/nav';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Button,
		Card,
		Checkbox,
		DeniedState,
		Notice,
		RadioGroup,
		SecretReveal,
		TextField,
		formatDateTime,
		toast
	} from '$lib/ui';
	import { can } from '$lib/features/common/access';
	import { newIdempotencyKey } from '$lib/features/common/data';
	import { actionError, fieldErrors } from '$lib/features/common/errors';
	import Fields from '$lib/features/common/Fields.svelte';
	import FormFooter from '$lib/features/common/FormFooter.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import { useUnsaved } from '$lib/features/common/unsaved.svelte';
	import PermissionEditor from '$lib/features/access/PermissionEditor.svelte';
	import { exceedsMaxLifetime, expiryFromDays } from '$lib/features/access/model';
	import { heldCapabilities, type Rule } from '$lib/features/access/permissions';
	import { accessKeys, catalogQuery } from '$lib/features/access/queries';
	import SettingsHeader from '$lib/features/settings/SettingsHeader.svelte';
	import { securitySettingsQuery } from '$lib/features/settings/queries';

	usePage({
		title: 'New API token',
		crumbs: [
			{ label: 'Settings', href: routes.settings() },
			{ label: 'API tokens', href: routes.apiTokens() },
			{ label: 'New token' }
		]
	});

	const qc = useQueryClient();
	const perms = createQuery(() => myPermissionsQuery());
	const access = $derived(accessOf(perms.data));
	const catalog = createQuery(() => catalogQuery());
	const security = createQuery(() => ({ ...securitySettingsQuery(), enabled: access.owner }));
	const maxDays = $derived(security.data?.apiTokenMaxLifetimeDays);
	const held = $derived(heldCapabilities(perms.data?.entries, access.owner, catalog.data));

	let name = $state('');
	let expiry = $state('30');
	let customDate = $state('');
	let neverExpires = $state(false);
	let grants = $state<Rule[]>([]);
	let busy = $state(false);
	let error = $state<unknown>(null);
	let created = $state<Schema<'CreateAPITokenOutputBody'> | null>(null);

	useUnsaved(
		() => 'New API token',
		() => !created && (grants.length > 0 || !!name)
	);

	const days = $derived(expiry === 'custom' ? null : Number(expiry));
	const expiresAt = $derived(
		neverExpires
			? undefined
			: expiry === 'custom'
				? customDate
					? new Date(customDate).toISOString()
					: undefined
				: expiryFromDays(Number(expiry))
	);
	const tooLong = $derived(days !== null && exceedsMaxLifetime(days, maxDays));
	const fields = $derived(fieldErrors(error));
	const canCreate = $derived(
		!!name.trim() && grants.length > 0 && (neverExpires || !!expiresAt) && !tooLong
	);

	async function create() {
		busy = true;
		error = null;
		const key = newIdempotencyKey();
		try {
			created = await withStepUp(() =>
				unwrap(
					api.POST('/api/v1/me/api-tokens', {
						params: { header: { 'Idempotency-Key': key } },
						body: {
							name: name.trim(),
							expiresAt,
							neverExpires: neverExpires || undefined,
							scopes: grants.map((g) => ({
								capability: g.capability,
								scope: g.scope
							}))
						}
					})
				)
			);
			await qc.invalidateQueries({ queryKey: accessKeys.myTokens() });
		} catch (e) {
			error = e;
		} finally {
			busy = false;
		}
	}
</script>

<Page narrow={!!created}>
	<SettingsHeader
		title="New API token"
		description="Grant only what the script needs. You can revoke the token at any time."
	/>
	{#if perms.data && !can(access, 'api_tokens.create')}
		<DeniedState
			level={2}
			title="You can't create API tokens."
			description="Ask the owner of this Docker Manager for the Create API tokens permission."
		/>
	{:else if created}
		<Card title="Your new token">
			<SecretReveal
				secret={created.token}
				label="API token"
				filename="docker-manager-api-token.txt"
				description="Store it in your script's secret store now. It works {created.apiToken
					.expiresAt
					? `until ${formatDateTime(created.apiToken.expiresAt)}`
					: 'until you revoke it'}. Send it as Authorization: Bearer <token>."
				confirmLabel="Done"
				onconfirm={() => {
					toast.success(`Created the API token ${created?.apiToken.name}`);
					void goto(routes.apiTokens());
				}}
			/>
		</Card>
	{:else}
		{#if error && !Object.keys(fields).length}
			<Notice tone="danger" title="The token was not created" live="alert">
				{actionError(error, {
					api_tokens_disabled:
						'API tokens are turned off for this Docker Manager. The owner can turn them on in the sign-in policy.'
				})}
			</Notice>
		{/if}
		<Card title="Token">
			<Fields columns={2}>
				<TextField
					label="Name"
					bind:value={name}
					required
					placeholder="Backup script"
					error={fields['body.name']}
				/>
				<div>
					<RadioGroup
						label="Expires"
						bind:value={expiry}
						options={[
							{ value: '7', label: 'In 7 days' },
							{ value: '30', label: 'In 30 days' },
							{ value: '90', label: 'In 90 days' },
							{ value: 'custom', label: 'On a date' }
						]}
						error={tooLong
							? `Tokens may live at most ${maxDays} days here.`
							: fields['body.expiresAt']}
					/>
					{#if expiry === 'custom'}
						<TextField label="Expiry date" type="date" bind:value={customDate} />
					{/if}
					{#if access.owner && security.data?.apiTokensNonExpiring}
						<Checkbox
							bind:checked={neverExpires}
							label="Never expires"
							description="Allowed by the sign-in policy. Revoke it when you no longer need it."
						/>
					{/if}
					{#if maxDays}<p class="muted small">
							This Docker Manager allows at most {maxDays} days.
						</p>{/if}
				</div>
			</Fields>
		</Card>
		<Card
			title="What the token may do"
			subtitle="Only actions you hold now are offered. The token never exceeds your current permissions: if yours shrink, so does the token."
		>
			<QueryView query={catalog} errorTitle="The permission catalog could not be loaded.">
				{#snippet children(cat)}
					<PermissionEditor
						catalog={cat}
						mode="token"
						rules={grants}
						{held}
						onchange={(r) => (grants = r.filter((x) => x.effect === 'allow'))}
					/>
				{/snippet}
			</QueryView>
			{#if fields['body.scopes']}<p class="error" role="alert">
					{fields['body.scopes']}
				</p>{/if}
		</Card>
		<FormFooter>
			<Button variant="ghost" href={routes.apiTokens()}>Cancel</Button>
			<Button variant="primary" loading={busy} disabled={!canCreate} onclick={create}>
				Create token{grants.length
					? ` with ${grants.length} ${grants.length === 1 ? 'grant' : 'grants'}`
					: ''}
			</Button>
		</FormFooter>
	{/if}
</Page>

<style>
	.small {
		font-size: var(--text-caption);
		margin-top: var(--space-2);
	}

	.error {
		margin-top: var(--space-3);
		color: var(--danger);
	}
</style>

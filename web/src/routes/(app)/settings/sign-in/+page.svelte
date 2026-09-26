<script lang="ts">
	// Sign-in policy (#16, #31; owner only): strict passwords, the factors
	// every account must use (with the enrollment consequences stated before
	// saving), invitation and reset lifetimes, and API tokens (on/off,
	// longest lifetime, tokens without expiry). Saving needs a step-up.
	import { untrack } from 'svelte';
	import { createQuery, useQueryClient } from '@tanstack/svelte-query';
	import { api, unwrap } from '$lib/api/client';
	import { myPermissionsQuery, queryKeys } from '$lib/api/queries';
	import { withStepUp } from '$lib/auth/stepup.svelte';
	import { routes } from '$lib/routes';
	import { usePage } from '$lib/shell/page.svelte';
	import {
		Button,
		Card,
		ConfirmDialog,
		DeniedState,
		RadioGroup,
		Switch,
		TextField,
		toast
	} from '$lib/ui';
	import { ifMatch } from '$lib/features/common/data';
	import { actionError, fieldErrors } from '$lib/features/common/errors';
	import Fields from '$lib/features/common/Fields.svelte';
	import FormFooter from '$lib/features/common/FormFooter.svelte';
	import Page from '$lib/features/common/Page.svelte';
	import QueryView from '$lib/features/common/QueryView.svelte';
	import { useUnsaved } from '$lib/features/common/unsaved.svelte';
	import SettingsHeader from '$lib/features/settings/SettingsHeader.svelte';
	import {
		FACTOR_DETAIL,
		FACTOR_POLICY,
		factorChangeConsequences,
		settingsChanges,
		type RequiredFactors
	} from '$lib/features/settings/model';
	import {
		securitySettingsQuery,
		settingsKeys,
		type SecuritySettings
	} from '$lib/features/settings/queries';

	usePage({
		title: 'Sign-in policy',
		crumbs: [{ label: 'Settings', href: routes.settings() }, { label: 'Sign-in policy' }]
	});

	const qc = useQueryClient();
	const perms = createQuery(() => myPermissionsQuery());
	const owner = $derived(!!perms.data?.owner);
	const settings = createQuery(() => ({
		...securitySettingsQuery(),
		enabled: owner,
		refetchOnWindowFocus: false
	}));

	let form = $state<SecuritySettings | null>(null);
	let base = $state<SecuritySettings | null>(null);
	$effect(() => {
		const s = settings.data;
		if (!s) return;
		untrack(() => {
			const dirty = form && base && settingsChanges(base, form).length > 0;
			base = s;
			if (!dirty) form = { ...s };
		});
	});

	const changes = $derived(base && form ? settingsChanges(base, form) : []);
	useUnsaved(
		() => 'Sign-in policy',
		() => changes.length > 0
	);
	let confirmOpen = $state(false);
	let error = $state<unknown>(null);
	const fields = $derived(fieldErrors(error));

	const consequences = $derived(
		base && form
			? [
					...changes,
					...factorChangeConsequences(
						base.requiredFactors,
						form.requiredFactors,
						form.enrollmentGraceHours
					)
				]
			: []
	);

	function num(v: string): number {
		const n = Math.floor(Number(v));
		return Number.isFinite(n) ? n : 0;
	}

	async function save() {
		if (!form || !base) return;
		error = null;
		const f = form;
		const b = base;
		try {
			const saved = await withStepUp(() =>
				unwrap(
					api.PATCH('/api/v1/settings/security', {
						params: { header: { 'If-Match': ifMatch(b.revision) } },
						body: {
							strictPasswords: f.strictPasswords,
							minPasswordLength: f.minPasswordLength,
							requiredFactors: f.requiredFactors,
							enrollmentGraceHours: f.enrollmentGraceHours,
							invitationTtlHours: f.invitationTtlHours,
							passwordResetTtlHours: f.passwordResetTtlHours,
							apiTokensEnabled: f.apiTokensEnabled,
							apiTokenMaxLifetimeDays: f.apiTokenMaxLifetimeDays,
							apiTokensNonExpiring: f.apiTokensNonExpiring
						}
					})
				)
			);
			base = saved;
			form = { ...saved };
			qc.setQueryData(settingsKeys.security, saved);
			await qc.invalidateQueries({ queryKey: queryKeys.session });
			toast.success('Saved the sign-in policy');
		} catch (e) {
			error = e;
			throw new Error(actionError(e), { cause: e });
		}
	}
</script>

<Page>
	<SettingsHeader
		title="Sign-in policy"
		description="How everyone signs in to this Docker Manager, and whether scripts may use API tokens."
	/>
	{#if perms.data && !owner}
		<DeniedState level={2} title="Only the owner changes the sign-in policy." />
	{:else}
		<QueryView query={settings} errorTitle="The sign-in policy could not be loaded.">
			{#snippet children(loaded)}
				{#if form && loaded}
					<Card
						title="Passwords"
						subtitle="Common and breached passwords are always refused. There are no composition rules and no forced changes."
					>
						<Fields columns={2}>
							<Switch
								label="Strict passwords"
								description="Enforce the minimum length below (otherwise at least 8 characters)."
								bind:checked={form.strictPasswords}
							/>
							<TextField
								label="Minimum length"
								type="number"
								min="8"
								value={String(form.minPasswordLength)}
								onchange={(e) =>
									form && (form.minPasswordLength = num(e.currentTarget.value))}
								description="Long passphrases are welcome."
								error={fields['body.minPasswordLength']}
							/>
						</Fields>
					</Card>
					<Card
						title="Required sign-in"
						subtitle="What every account must use. Accounts without it get a limited session to add it."
					>
						<Fields>
							<RadioGroup
								label="Every account signs in with"
								bind:value={form.requiredFactors}
								options={(Object.keys(FACTOR_POLICY) as RequiredFactors[]).map(
									(k) => ({
										value: k,
										label: FACTOR_POLICY[k],
										description: FACTOR_DETAIL[k]
									})
								)}
							/>
							<TextField
								label="Time to add required factors (hours)"
								type="number"
								min="1"
								value={String(form.enrollmentGraceHours)}
								onchange={(e) =>
									form &&
									(form.enrollmentGraceHours = num(e.currentTarget.value))}
								description="How long accounts may still sign in to enroll after the policy changes or their factors are reset."
								error={fields['body.enrollmentGraceHours']}
							/>
						</Fields>
					</Card>
					<Card title="Invitations and resets">
						<Fields columns={2}>
							<TextField
								label="Invitations expire after (hours)"
								type="number"
								min="1"
								value={String(form.invitationTtlHours)}
								onchange={(e) =>
									form && (form.invitationTtlHours = num(e.currentTarget.value))}
								error={fields['body.invitationTtlHours']}
							/>
							<TextField
								label="Password reset links expire after (hours)"
								type="number"
								min="1"
								value={String(form.passwordResetTtlHours)}
								onchange={(e) =>
									form &&
									(form.passwordResetTtlHours = num(e.currentTarget.value))}
								error={fields['body.passwordResetTtlHours']}
							/>
						</Fields>
					</Card>
					<Card
						title="API tokens"
						subtitle="Tokens act with at most their user's current permissions and never count as a sign-in factor."
					>
						<Fields columns={2}>
							<Switch
								label="Allow API tokens"
								description="Off stops every token at once (they work again when turned back on; revoke them to end them)."
								bind:checked={form.apiTokensEnabled}
							/>
							<TextField
								label="Longest lifetime (days)"
								type="number"
								min="1"
								value={String(form.apiTokenMaxLifetimeDays)}
								onchange={(e) =>
									form &&
									(form.apiTokenMaxLifetimeDays = num(e.currentTarget.value))}
								description="For tokens created afterwards."
								error={fields['body.apiTokenMaxLifetimeDays']}
							/>
							<Switch
								label="Allow tokens without expiry"
								description="Off by default. Tokens that never expire must be revoked by hand."
								bind:checked={form.apiTokensNonExpiring}
							/>
						</Fields>
					</Card>
					<FormFooter>
						<Button
							variant="ghost"
							disabled={!changes.length}
							onclick={() => base && (form = { ...base })}>Discard changes</Button
						>
						<Button
							variant="primary"
							disabled={!changes.length}
							onclick={() => (confirmOpen = true)}>Save sign-in policy</Button
						>
					</FormFooter>
				{/if}
			{/snippet}
		</QueryView>
	{/if}
</Page>

<ConfirmDialog
	bind:open={confirmOpen}
	title="Save the sign-in policy?"
	{consequences}
	confirmLabel="Save sign-in policy"
	tone={base && form && base.requiredFactors !== form.requiredFactors ? 'danger' : 'default'}
	onconfirm={save}
/>

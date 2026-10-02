<script lang="ts">
	// Sign-in policy (#16, #31; owner only): strict passwords, the factors
	// every account must use (with the enrollment consequences stated before
	// saving), whether the sign-in page offers Stay Signed In, invitation
	// and reset lifetimes, and API tokens (on/off, longest lifetime, tokens
	// without expiry). Saving needs a step-up.
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
		staySignedInConsequences,
		type RequiredFactors
	} from '$lib/features/settings/model';
	import {
		securitySettingsQuery,
		settingsKeys,
		type SecuritySettings
	} from '$lib/features/settings/queries';

	usePage({
		title: 'Sign-In Policy',
		crumbs: [{ label: 'Settings', href: routes.settings() }, { label: 'Sign-In Policy' }]
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
					),
					...staySignedInConsequences(base.allowStaySignedIn, form.allowStaySignedIn)
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
							allowStaySignedIn: f.allowStaySignedIn,
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
		title="Sign-In Policy"
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
								label="Strict Passwords"
								description="Enforce the minimum length below (otherwise at least 8 characters)."
								bind:checked={form.strictPasswords}
							/>
							<TextField
								label="Minimum Length"
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
						title="Required Sign-In"
						subtitle="What every account must use. Accounts without it get a limited session to add it."
					>
						<Fields>
							<RadioGroup
								label="Every Account Signs In With"
								bind:value={form.requiredFactors}
								options={(Object.keys(FACTOR_POLICY) as RequiredFactors[]).map(
									(k) => ({
										value: k,
										label: FACTOR_POLICY[k],
										description: FACTOR_DETAIL[k]
									})
								)}
							/>
							<!-- Two columns, like the other number fields of the policy. -->
							<Fields columns={2}>
								<TextField
									label="Time to Add Required Factors (Hours)"
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
						</Fields>
					</Card>
					<Card
						title="Staying Signed In"
						subtitle="By default a browser is signed out after 8 hours without activity, 24 hours at most, and when it closes."
					>
						<Fields columns={2}>
							<Switch
								label="Allow Stay Signed In"
								description="People can keep a device signed in for longer: by default 30 days without activity, a year at most. Off moves those devices back to the normal limits."
								bind:checked={form.allowStaySignedIn}
							/>
						</Fields>
					</Card>
					<Card title="Invitations and Resets">
						<Fields columns={2}>
							<TextField
								label="Invitations Expire After (Hours)"
								type="number"
								min="1"
								value={String(form.invitationTtlHours)}
								onchange={(e) =>
									form && (form.invitationTtlHours = num(e.currentTarget.value))}
								error={fields['body.invitationTtlHours']}
							/>
							<TextField
								label="Password Reset Links Expire After (Hours)"
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
						title="API Tokens"
						subtitle="Tokens act with at most their user's current permissions and never count as a sign-in factor."
					>
						<Fields columns={2}>
							<Switch
								label="Allow API Tokens"
								description="Off stops every token at once (they work again when turned back on; revoke them to end them)."
								bind:checked={form.apiTokensEnabled}
							/>
							<TextField
								label="Longest Lifetime (Days)"
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
								label="Allow Tokens Without Expiry"
								description="Off by default. Tokens that never expire must be revoked by hand."
								bind:checked={form.apiTokensNonExpiring}
							/>
						</Fields>
					</Card>
					<FormFooter>
						<Button
							variant="ghost"
							disabled={!changes.length}
							onclick={() => base && (form = { ...base })}>Discard Changes</Button
						>
						<Button
							variant="primary"
							disabled={!changes.length}
							onclick={() => (confirmOpen = true)}>Save Sign-In Policy</Button
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
	confirmLabel="Save Sign-In Policy"
	tone={base && form && base.requiredFactors !== form.requiredFactors ? 'danger' : 'default'}
	onconfirm={save}
/>

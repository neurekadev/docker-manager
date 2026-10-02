<script lang="ts">
	// Set up an authenticator app (#16, RFC 6238): the otpauth URI is drawn
	// as a QR code in the browser (never sent anywhere), with the secret for
	// manual entry; a correct code activates it within 10 minutes.
	import { useQueryClient } from '@tanstack/svelte-query';
	import { api, unwrap } from '$lib/api/client';
	import { queryKeys } from '$lib/api/queries';
	import { qrPath } from '$lib/auth/qr';
	import {
		Button,
		CopyButton,
		Notice,
		TextField,
		errorView,
		formatDateTime,
		toast
	} from '$lib/ui';
	import FormFooter from '$lib/features/common/FormFooter.svelte';

	let { ondone }: { ondone: () => void } = $props();

	const qc = useQueryClient();
	let enrollment = $state<{ secret: string; uri: string; expiresAt: string } | null>(null);
	let code = $state('');
	let busy = $state(false);
	let message = $state<string | null>(null);
	const qr = $derived(enrollment ? qrPath(enrollment.uri) : null);

	async function start() {
		busy = true;
		message = null;
		try {
			enrollment = await unwrap(api.POST('/api/v1/auth/totp/enrollments'));
		} catch (e) {
			message = errorView(e).message;
		} finally {
			busy = false;
		}
	}

	async function verify(e: SubmitEvent) {
		e.preventDefault();
		busy = true;
		message = null;
		try {
			const s = await unwrap(
				api.POST('/api/v1/auth/totp/enrollments/verifications', {
					body: { code: code.replace(/\s/g, '') }
				})
			);
			qc.setQueryData(queryKeys.session, s);
			enrollment = null;
			toast.success('Turned on the authenticator app', {
				body: 'Sign-ins with your password now also ask for its code.'
			});
			ondone();
		} catch (err) {
			const v = errorView(err);
			message =
				v.code === 'invalid_credentials'
					? 'That code did not match. Check the time on your phone and enter the current code.'
					: v.code === 'no_pending_flow'
						? 'The setup timed out. Start again.'
						: v.message;
			if (v.code === 'no_pending_flow') enrollment = null;
		} finally {
			busy = false;
			code = '';
		}
	}
</script>

{#if !enrollment}
	<Button variant="primary" loading={busy} onclick={start}>Set Up Authenticator App</Button>
	{#if message}<Notice tone="danger" title="Not Started" live="alert">{message}</Notice>{/if}
{:else}
	<div class="setup">
		{#if qr}
			<svg
				class="qr"
				viewBox="0 0 {qr.size} {qr.size}"
				role="img"
				aria-label="QR code of the authenticator setup"
			>
				<rect width={qr.size} height={qr.size} class="qr-bg" />
				<path d={qr.path} class="qr-fg" />
			</svg>
		{/if}
		<div class="steps">
			<p>Scan the code with your authenticator app, or enter the secret by hand:</p>
			<p class="secret">
				<span class="mono">{enrollment.secret}</span>
				<CopyButton value={enrollment.secret} what="secret" />
			</p>
			<form onsubmit={verify} novalidate>
				<TextField
					label="Code From the App"
					bind:value={code}
					inputmode="numeric"
					autocomplete="one-time-code"
					description="Confirm before {formatDateTime(enrollment.expiresAt)}."
					required
				/>
				{#if message}<Notice tone="danger" title="Not Turned On" live="alert"
						>{message}</Notice
					>{/if}
				<FormFooter>
					<Button variant="ghost" onclick={() => (enrollment = null)}>Cancel</Button>
					<Button
						variant="primary"
						type="submit"
						loading={busy}
						disabled={code.replace(/\s/g, '').length < 6}>Turn On</Button
					>
				</FormFooter>
			</form>
		</div>
	</div>
{/if}

<style>
	.setup {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-5);
		align-items: flex-start;
	}

	.qr {
		width: 184px;
		height: 184px;
		border-radius: var(--radius-md);
	}

	.qr-bg {
		fill: var(--text-strong);
	}

	.qr-fg {
		fill: var(--surface-canvas);
	}

	.steps {
		display: grid;
		gap: var(--space-3);
		flex: 1 1 280px;
		min-width: 0;
	}

	.secret {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: var(--space-2);
		overflow-wrap: anywhere;
	}
</style>

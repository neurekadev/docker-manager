<script lang="ts">
	// Where backups live (#10): a local directory on the manager or on one
	// environment's agent (in a folder that host allows for backups: its
	// *_BACKUP_LOCAL_ROOTS setting, named only in the documentation), or an
	// S3 bucket and prefix. S3 secrets are write-only: typed here, never
	// shown.
	import { PasswordField, RadioGroup, Select, Switch, TextField } from '$lib/ui';
	import Fields from '$lib/features/common/Fields.svelte';
	import type { Destination } from './destination';

	interface Props {
		value: Destination;
		/** Offer the executor (manager or an environment) for local paths. */
		executors?: { value: string; label: string }[];
		errors?: Record<string, string>;
		/** S3 credentials are optional (editing keeps the stored ones). */
		credentialsOptional?: boolean;
		localDescription?: string;
	}

	let {
		value = $bindable(),
		executors,
		errors = {},
		credentialsOptional = false,
		localDescription = 'An absolute path outside what it backs up, in a folder the host allows for backups.'
	}: Props = $props();
</script>

<Fields>
	<RadioGroup
		label="Storage"
		bind:value={value.kind}
		options={[
			{
				value: 'local',
				label: 'Local Directory',
				description: 'On the manager or one environment. Recovery needs that disk.'
			},
			{
				value: 's3',
				label: 'S3-Compatible Storage',
				description: 'AWS S3, MinIO, Backblaze B2, Wasabi and others.'
			}
		]}
	/>
	{#if value.kind === 'local'}
		{#if executors}
			<Select
				label="Written By"
				info="Local repositories live on one host; each environment backs up to its own."
				options={executors}
				bind:value={value.executor}
				error={errors['body.executor']}
			/>
		{/if}
		<TextField
			label="Directory"
			mono
			bind:value={value.path}
			placeholder="/backups/docker-manager"
			description={localDescription}
			required
			error={errors['body.path']}
		/>
	{:else}
		<Fields columns={2}>
			<TextField
				label="Endpoint"
				mono
				bind:value={value.endpoint}
				placeholder="https://s3.eu-central-1.amazonaws.com"
				required
				error={errors['body.endpoint']}
			/>
			<TextField
				label="Region"
				optional
				bind:value={value.region}
				placeholder="eu-central-1"
				error={errors['body.region']}
			/>
			<TextField
				label="Bucket"
				mono
				bind:value={value.bucket}
				required
				error={errors['body.bucket']}
			/>
			<TextField
				label="Prefix"
				mono
				optional
				description="A folder inside the bucket."
				bind:value={value.prefix}
				placeholder="docker-manager"
				error={errors['body.prefix']}
			/>
			<TextField
				label="Access Key ID"
				mono
				bind:value={value.accessKeyId}
				autocomplete="off"
				required={!credentialsOptional}
				optional={credentialsOptional}
				description={credentialsOptional
					? 'Leave empty to keep the stored key pair.'
					: undefined}
				error={errors['body.accessKeyId']}
			/>
			<PasswordField
				label="Secret Access Key"
				bind:value={value.secretAccessKey}
				autocomplete="off"
				required={!credentialsOptional}
				description="Write-only: Docker Manager never shows it again."
				error={errors['body.secretAccessKey']}
			/>
		</Fields>
		<Switch
			label="Path-Style Addressing"
			info="On for MinIO and most self-hosted S3; off for AWS virtual-hosted buckets."
			bind:checked={value.pathStyle}
		/>
	{/if}
</Fields>

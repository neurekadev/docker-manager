<script lang="ts">
	// Where backups live (#10, #244): an S3 bucket and prefix. The secret
	// is write-only: typed here, never shown.
	import { PasswordField, Switch, TextField } from '$lib/ui';
	import Fields from '$lib/features/common/Fields.svelte';
	import type { Destination } from './destination';

	interface Props {
		value: Destination;
		errors?: Record<string, string>;
		/** The credentials are optional (editing keeps the stored ones). */
		credentialsOptional?: boolean;
	}

	let { value = $bindable(), errors = {}, credentialsOptional = false }: Props = $props();
</script>

<Fields>
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
</Fields>

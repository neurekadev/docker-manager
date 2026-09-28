<script lang="ts">
	// A backup repository's compression (#10): Automatic, Maximum or Off,
	// for local and S3 repositories alike. A change applies to data backed
	// up afterwards.
	import { RadioGroup } from '$lib/ui';
	import { COMPRESSION_CHANGE_NOTE, COMPRESSION_OPTIONS } from './model';

	interface Props {
		/** auto, max or off (CompressionMode). */
		value: string;
		error?: string;
		description?: string;
	}

	let { value = $bindable(), error, description = COMPRESSION_CHANGE_NOTE }: Props = $props();
</script>

<RadioGroup label="Compression" {description} options={COMPRESSION_OPTIONS} bind:value {error} />

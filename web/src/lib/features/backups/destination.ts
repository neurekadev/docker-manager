// A backup destination being entered (#10, #24, #244): an S3 bucket.

export interface Destination {
	endpoint: string;
	bucket: string;
	prefix: string;
	region: string;
	pathStyle: boolean;
	accessKeyId: string;
	secretAccessKey: string;
}

export function emptyDestination(): Destination {
	return {
		endpoint: '',
		bucket: '',
		prefix: '',
		region: '',
		pathStyle: true,
		accessKeyId: '',
		secretAccessKey: ''
	};
}

/** The destination is complete enough to send. */
export function destinationReady(d: Destination, requireCredentials = true): boolean {
	return (
		!!d.endpoint.trim() &&
		!!d.bucket.trim() &&
		(!requireCredentials || (!!d.accessKeyId.trim() && !!d.secretAccessKey))
	);
}

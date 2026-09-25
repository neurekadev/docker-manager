// A backup destination being entered (#10, #24): local directory or S3.

export interface Destination {
	kind: 'local' | 's3';
	/** Local: manager or an environment ID (repository creation only). */
	executor: string;
	path: string;
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
		kind: 'local',
		executor: 'manager',
		path: '',
		endpoint: '',
		bucket: '',
		prefix: '',
		region: '',
		pathStyle: true,
		accessKeyId: '',
		secretAccessKey: ''
	};
}

/** An absolute path on the host (POSIX; a drive letter on Windows test hosts). */
export function isAbsolutePath(p: string): boolean {
	return /^(\/|[A-Za-z]:[/\\])/.test(p.trim());
}

/** The destination is complete enough to send. */
export function destinationReady(d: Destination, requireCredentials = true): boolean {
	if (d.kind === 'local') return isAbsolutePath(d.path);
	return (
		!!d.endpoint.trim() &&
		!!d.bucket.trim() &&
		(!requireCredentials || (!!d.accessKeyId.trim() && !!d.secretAccessKey))
	);
}

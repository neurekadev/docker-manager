// The caller's own sign-in factors (#16) for Svelte Query: passkeys and
// the recovery codes' state (live topic 'permissions', resource 'me').
import { queryOptions } from '@tanstack/svelte-query';
import { api, unwrap, type ApiClient, type Schema } from '$lib/api/client';
import { liveKeys } from '$lib/live/keys';

export type Passkey = Schema<'Passkey'>;

export const profileKeys = {
	passkeys: liveKeys.item('permissions', 'me', 'passkeys'),
	recoveryCodes: liveKeys.item('permissions', 'me', 'recovery-codes')
};

export function passkeysQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: profileKeys.passkeys,
		queryFn: async ({ signal }): Promise<Passkey[]> =>
			(await unwrap(client.GET('/api/v1/me/passkeys', { signal }))).items
	});
}

export function recoveryCodesQuery(client: ApiClient = api) {
	return queryOptions({
		queryKey: profileKeys.recoveryCodes,
		queryFn: ({ signal }) => unwrap(client.GET('/api/v1/me/recovery-codes', { signal }))
	});
}

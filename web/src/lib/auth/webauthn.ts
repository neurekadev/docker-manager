// WebAuthn glue (#16): the manager sends PublicKeyCredential options as
// JSON ({"publicKey": {...}} with base64url binary fields) and expects the
// credential back as JSON. Browsers without the JSON helpers
// (parseCreationOptionsFromJSON / toJSON) get the same conversion by hand.

export function b64urlToBuffer(s: string): ArrayBuffer {
	const pad = '='.repeat((4 - (s.length % 4)) % 4);
	const bin = atob((s + pad).replaceAll('-', '+').replaceAll('_', '/'));
	const out = new Uint8Array(bin.length);
	for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
	return out.buffer;
}

export function bufferToB64url(b: ArrayBuffer | ArrayBufferView): string {
	const bytes =
		b instanceof ArrayBuffer
			? new Uint8Array(b)
			: new Uint8Array(b.buffer, b.byteOffset, b.byteLength);
	let bin = '';
	for (const x of bytes) bin += String.fromCharCode(x);
	return btoa(bin).replaceAll('+', '-').replaceAll('/', '_').replace(/=+$/, '');
}

type Json = Record<string, unknown>;

function unwrapPublicKey(options: unknown): Json {
	const o = (options ?? {}) as Json;
	return ((o.publicKey as Json | undefined) ?? o) as Json;
}

function credList(list: unknown): PublicKeyCredentialDescriptor[] | undefined {
	if (!Array.isArray(list)) return undefined;
	return list.map((c: Json) => ({
		...(c as object),
		type: 'public-key',
		id: b64urlToBuffer(String(c.id))
	})) as PublicKeyCredentialDescriptor[];
}

/** Server JSON → options for navigator.credentials.create(). */
export function creationOptions(json: unknown): PublicKeyCredentialCreationOptions {
	const pk = unwrapPublicKey(json);
	const user = pk.user as Json;
	return {
		...(pk as object),
		challenge: b64urlToBuffer(String(pk.challenge)),
		user: { ...(user as object), id: b64urlToBuffer(String(user.id)) },
		excludeCredentials: credList(pk.excludeCredentials)
	} as PublicKeyCredentialCreationOptions;
}

/** Server JSON → options for navigator.credentials.get(). */
export function requestOptions(json: unknown): PublicKeyCredentialRequestOptions {
	const pk = unwrapPublicKey(json);
	return {
		...(pk as object),
		challenge: b64urlToBuffer(String(pk.challenge)),
		allowCredentials: credList(pk.allowCredentials)
	} as PublicKeyCredentialRequestOptions;
}

/** A PublicKeyCredential as the JSON the manager verifies. */
export function credentialToJSON(cred: PublicKeyCredential): Json {
	const c = cred as PublicKeyCredential & { toJSON?: () => Json };
	if (typeof c.toJSON === 'function') {
		try {
			return c.toJSON();
		} catch {
			// fall through to the manual conversion
		}
	}
	const r = cred.response as AuthenticatorAttestationResponse & AuthenticatorAssertionResponse;
	const response: Json = { clientDataJSON: bufferToB64url(r.clientDataJSON) };
	if ('attestationObject' in r && r.attestationObject) {
		response.attestationObject = bufferToB64url(r.attestationObject);
		if (typeof r.getTransports === 'function') response.transports = r.getTransports();
	}
	if ('authenticatorData' in r && r.authenticatorData) {
		response.authenticatorData = bufferToB64url(r.authenticatorData);
		response.signature = bufferToB64url(r.signature);
		if (r.userHandle) response.userHandle = bufferToB64url(r.userHandle);
	}
	return {
		id: cred.id,
		rawId: bufferToB64url(cred.rawId),
		type: cred.type,
		response,
		authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
		clientExtensionResults: cred.getClientExtensionResults?.() ?? {}
	};
}

/** Passkeys are usable in this browser (secure context + API present). */
export function passkeysSupported(): boolean {
	return (
		typeof window !== 'undefined' && window.isSecureContext && 'PublicKeyCredential' in window
	);
}

/** A user cancelled or timed out the browser's passkey prompt. */
export function isCancelled(e: unknown): boolean {
	return e instanceof DOMException && (e.name === 'NotAllowedError' || e.name === 'AbortError');
}

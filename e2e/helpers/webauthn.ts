// Chromium virtual WebAuthn authenticator via the DevTools protocol
// (WebAuthn.enable / WebAuthn.addVirtualAuthenticator), for passkey tests
// (#16, #29). Chromium only.
import type { CDPSession, Page } from '@playwright/test';

export interface VirtualAuthenticatorOptions {
	protocol?: 'ctap2' | 'u2f';
	transport?: 'internal' | 'usb' | 'nfc' | 'ble' | 'hybrid';
	hasResidentKey?: boolean;
	hasUserVerification?: boolean;
	isUserVerified?: boolean;
	automaticPresenceSimulation?: boolean;
}

export interface VirtualCredential {
	credentialId: string;
	isResidentCredential: boolean;
	rpId?: string;
	userHandle?: string;
	signCount: number;
}

export interface VirtualAuthenticator {
	cdp: CDPSession;
	authenticatorId: string;
	/** Credentials currently stored on the authenticator. */
	credentials(): Promise<VirtualCredential[]>;
	/** Simulate a user who fails (or passes) user verification. */
	setUserVerified(verified: boolean): Promise<void>;
	/** Drop all stored credentials (e.g. a lost device). */
	clear(): Promise<void>;
	remove(): Promise<void>;
}

/**
 * Attaches a platform-like passkey authenticator (CTAP2, internal
 * transport, resident keys, user verification) to the page's browser
 * context. Call before the page invokes navigator.credentials.
 */
export async function addVirtualAuthenticator(
	page: Page,
	options: VirtualAuthenticatorOptions = {}
): Promise<VirtualAuthenticator> {
	const cdp = await page.context().newCDPSession(page);
	await cdp.send('WebAuthn.enable', { enableUI: false });
	const { authenticatorId } = await cdp.send('WebAuthn.addVirtualAuthenticator', {
		options: {
			protocol: options.protocol ?? 'ctap2',
			transport: options.transport ?? 'internal',
			hasResidentKey: options.hasResidentKey ?? true,
			hasUserVerification: options.hasUserVerification ?? true,
			isUserVerified: options.isUserVerified ?? true,
			automaticPresenceSimulation: options.automaticPresenceSimulation ?? true
		}
	});
	return {
		cdp,
		authenticatorId,
		async credentials() {
			const { credentials } = await cdp.send('WebAuthn.getCredentials', { authenticatorId });
			return credentials as VirtualCredential[];
		},
		async setUserVerified(verified: boolean) {
			await cdp.send('WebAuthn.setUserVerified', {
				authenticatorId,
				isUserVerified: verified
			});
		},
		async clear() {
			await cdp.send('WebAuthn.clearCredentials', { authenticatorId });
		},
		async remove() {
			await cdp.send('WebAuthn.removeVirtualAuthenticator', { authenticatorId });
			await cdp.detach();
		}
	};
}

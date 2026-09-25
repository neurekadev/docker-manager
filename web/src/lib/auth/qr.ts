// QR codes rendered in the browser (#16 TOTP enrollment): the otpauth://
// URI never leaves the page (no image service). uqr (MIT) encodes it; we
// draw the modules as one SVG path with the design's colours and a quiet
// zone, so it scans reliably on the dark UI (dark modules on light).
import { encode } from 'uqr';

export interface QrSvg {
	/** viewBox size in modules, including the quiet zone. */
	size: number;
	/** SVG path data of the dark modules. */
	path: string;
}

export function qrPath(text: string, border = 3): QrSvg {
	const { data, size } = encode(text, { ecc: 'M', border: 0 });
	let path = '';
	for (let y = 0; y < size; y++) {
		for (let x = 0; x < size; x++) {
			if (data[y][x]) path += `M${x + border} ${y + border}h1v1h-1z`;
		}
	}
	return { size: size + 2 * border, path };
}

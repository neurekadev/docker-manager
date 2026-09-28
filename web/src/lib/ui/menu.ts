// Menu model shared by Menu, ContextMenu and SplitButton (#22).
import type { IconComponent } from '$lib/design/icons';

export interface MenuItem {
	label: string;
	icon?: IconComponent;
	/** Called when the item is chosen (click, Enter, Space). */
	onSelect?: () => void;
	/** Navigates instead (rendered as a link). */
	href?: string;
	/** danger: destructive actions (red text); they still confirm in a dialog. */
	tone?: 'default' | 'danger';
	disabled?: boolean;
	/**
	 * A short line under the label (the item's accessible description),
	 * e.g. why a disabled item is off.
	 */
	description?: string;
	/** A keyboard shortcut hint, e.g. "⌘K". */
	shortcut?: string;
}

export interface MenuSeparator {
	separator: true;
}

export interface MenuHeading {
	heading: string;
}

export type MenuEntry = MenuItem | MenuSeparator | MenuHeading;

export function isSeparator(e: MenuEntry): e is MenuSeparator {
	return 'separator' in e;
}

export function isHeading(e: MenuEntry): e is MenuHeading {
	return 'heading' in e;
}

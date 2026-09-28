// Docker Manager's CodeMirror theme (#22): the mockup's editor colours (keys in
// blue, strings amber, URLs red, muted punctuation, JetBrains Mono) on the
// panel surface. Loaded only through import() from ./index.ts.
import { HighlightStyle, syntaxHighlighting } from '@codemirror/language';
import { EditorView } from '@codemirror/view';
import { tags as t } from '@lezer/highlight';
import { EDITOR_COLORS as c } from './palette';

const theme = EditorView.theme(
	{
		'&': {
			color: c.text,
			backgroundColor: c.background,
			fontSize: '13px',
			height: '100%'
		},
		'.cm-scroller': {
			fontFamily: c.fontMono,
			lineHeight: '20px'
		},
		'.cm-content': { caretColor: c.caret, padding: '8px 0' },
		'.cm-cursor, .cm-dropCursor': { borderLeftColor: c.caret, borderLeftWidth: '2px' },
		// As specific as CodeMirror's own focused-selection rule (a near-black
		// #233 in dark mode), so the visible selection colour always wins.
		'&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection':
			{
				backgroundColor: c.selection
			},
		// Translucent: the selection is drawn below the lines and must show
		// through the current line.
		'.cm-activeLine': { backgroundColor: c.activeLineOverlay },
		'.cm-gutters': {
			backgroundColor: c.background,
			color: c.gutter,
			border: 'none',
			paddingRight: '8px'
		},
		'.cm-activeLineGutter': { backgroundColor: 'transparent', color: c.gutterActive },
		'.cm-lineNumbers .cm-gutterElement': { minWidth: '32px', paddingRight: '12px' },
		'.cm-foldPlaceholder': {
			backgroundColor: c.surfaceRaised,
			border: `1px solid ${c.border}`,
			color: c.muted
		},
		'.cm-tooltip': {
			backgroundColor: c.surfaceRaised,
			border: `1px solid ${c.border}`,
			color: c.text
		},
		'.cm-panels': { backgroundColor: c.surfaceRaised, color: c.text },
		'.cm-panels.cm-panels-top': { borderBottom: `1px solid ${c.border}` },
		'.cm-panels.cm-panels-bottom': { borderTop: `1px solid ${c.border}` },
		// The search and replace panel (#15): inputs and buttons like the
		// app's controls.
		'.cm-panel.cm-search': {
			padding: '6px 8px',
			fontFamily: c.fontSans,
			fontSize: '12px'
		},
		'.cm-textfield': {
			backgroundColor: c.background,
			border: `1px solid ${c.border}`,
			borderRadius: '6px',
			color: c.text,
			padding: '3px 6px',
			fontSize: '12px'
		},
		'.cm-textfield:focus': { outline: `2px solid ${c.caret}`, outlineOffset: '0' },
		'.cm-button': {
			backgroundImage: 'none',
			backgroundColor: c.background,
			border: `1px solid ${c.border}`,
			borderRadius: '6px',
			color: c.text,
			padding: '3px 8px',
			fontSize: '12px'
		},
		'.cm-button:focus-visible': { outline: `2px solid ${c.caret}`, outlineOffset: '1px' },
		'.cm-panel.cm-search label': { color: c.muted, fontSize: '12px' },
		'.cm-panel.cm-search [name=close]': { color: c.muted, fontSize: '16px' },
		'.cm-searchMatch': { backgroundColor: 'rgba(245, 181, 68, 0.25)' },
		'.cm-searchMatch.cm-searchMatch-selected': { backgroundColor: 'rgba(245, 181, 68, 0.45)' },
		'.cm-matchingBracket, .cm-nonmatchingBracket': {
			backgroundColor: c.selection,
			outline: 'none'
		},
		'&.cm-focused': { outline: 'none' }
	},
	{ dark: true }
);

const highlight = HighlightStyle.define([
	{ tag: [t.propertyName, t.definition(t.propertyName), t.attributeName], color: c.key },
	{ tag: [t.string, t.special(t.string)], color: c.string },
	{ tag: [t.number, t.bool, t.null, t.atom], color: c.number },
	{ tag: [t.url, t.link], color: c.url },
	{ tag: [t.comment, t.lineComment, t.blockComment], color: c.comment, fontStyle: 'italic' },
	{ tag: [t.punctuation, t.separator, t.bracket, t.operator], color: c.punctuation },
	{ tag: [t.keyword, t.typeName, t.labelName], color: c.keyword },
	{ tag: t.invalid, color: c.url }
]);

/** The theme and highlighting as one extension. */
export const dockerManagerEditorTheme = [theme, syntaxHighlighting(highlight)];

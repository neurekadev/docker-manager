// Docker Manager component library (#22). Import from '$lib/ui':
//
//   import { Button, Card, StatusBadge, toast } from '$lib/ui';
//
// Usage rules, props and examples: docs/internal/design/README.md. Heavy
// libraries stay lazy: CodeEditor, Sparkline and TerminalView load
// CodeMirror, ECharts and xterm.js only when they mount.

// Actions
export { default as Button } from './Button.svelte';
export type { ButtonVariant, ButtonSize } from './Button.svelte';
export { default as IconButton } from './IconButton.svelte';
export { default as SplitButton } from './SplitButton.svelte';
export type { SplitButtonVariant } from './SplitButton.svelte';
export { default as CopyButton } from './CopyButton.svelte';
export { default as Menu } from './Menu.svelte';
export { default as ContextMenu } from './ContextMenu.svelte';
export type { MenuEntry, MenuItem, MenuSeparator, MenuHeading } from './menu';

// Display
export { default as Badge } from './Badge.svelte';
export type { BadgeTone } from './Badge.svelte';
export { default as Chip } from './Chip.svelte';
export { default as StatusBadge } from './StatusBadge.svelte';
export { statusInfo, type StatusInfo } from './status';
export { default as Card } from './Card.svelte';
export { default as KpiCard } from './KpiCard.svelte';
export { default as IconTile } from './IconTile.svelte';
export { default as Meter } from './Meter.svelte';
export { default as Uptime } from './Uptime.svelte';
export { clock, TICK_MS } from './clock.svelte';
export { default as Kbd } from './Kbd.svelte';
export { default as PageHeader } from './PageHeader.svelte';
export type { MetaItem } from './PageHeader.svelte';
export { default as Table } from './Table.svelte';
export type { Column, SortState, SortDirection } from './table';
export { default as Tabs } from './Tabs.svelte';
export type { TabItem } from './Tabs.svelte';
export { default as TabNav } from './TabNav.svelte';
export type { TabLink } from './TabNav.svelte';
export { default as Breadcrumbs } from './Breadcrumbs.svelte';
export type { Crumb } from './Breadcrumbs.svelte';
export { default as DiffView } from './DiffView.svelte';
export { diffText, diffLines, splitLines, type DiffResult, type DiffLine } from './diff';
export { default as Skeleton } from './Skeleton.svelte';
export { default as Spinner } from './Spinner.svelte';

// Forms
export { default as Field } from './Field.svelte';
export type { FieldControl } from './Field.svelte';
export { default as TextField } from './TextField.svelte';
export { default as PasswordField } from './PasswordField.svelte';
export { default as TextArea } from './TextArea.svelte';
export { default as Select } from './Select.svelte';
export type { SelectOption } from './Select.svelte';
export { default as Combobox } from './Combobox.svelte';
export { default as SuggestField } from './SuggestField.svelte';
export { default as Checkbox } from './Checkbox.svelte';
export { default as Switch } from './Switch.svelte';
export { default as RadioGroup } from './RadioGroup.svelte';
export type { RadioOption } from './RadioGroup.svelte';
export { default as TriState } from './TriState.svelte';
export type { TriValue } from './TriState.svelte';
export { default as CronField } from './CronField.svelte';
export {
	describeCron,
	parseCronPreset,
	buildCron,
	type CronPreset,
	type CronPresetKind
} from './cron';

// Overlays
export { default as Dialog } from './Dialog.svelte';
export { default as ConfirmDialog } from './ConfirmDialog.svelte';
export { default as DestructiveConfirm } from './DestructiveConfirm.svelte';
export type { AffectedResource } from './DestructiveConfirm.svelte';
export { default as TypeToConfirm } from './TypeToConfirm.svelte';
export { default as Drawer } from './Drawer.svelte';
export { default as Popover } from './Popover.svelte';
export { default as Tooltip } from './Tooltip.svelte';
export { default as InfoTip } from './InfoTip.svelte';
export { default as TooltipLayer } from './TooltipLayer.svelte';
export { placeTooltip, tooltipAnchor, type Placement } from './tooltip';

// Feedback and states
export { default as Toaster } from './Toaster.svelte';
export { toast, Toasts, type Toast, type ToastTone } from './toast.svelte';
export { default as Notice } from './Notice.svelte';
export { default as EmptyState } from './EmptyState.svelte';
export { default as ErrorState } from './ErrorState.svelte';
export { default as DeniedState } from './DeniedState.svelte';
export { default as OfflineEnvironment } from './OfflineEnvironment.svelte';
export { default as JobProgress } from './JobProgress.svelte';
export { default as SecretReveal } from './SecretReveal.svelte';
export { default as StepWizard } from './StepWizard.svelte';
export type { WizardStep } from './StepWizard.svelte';
export { errorView, errorMessage, fieldError, type ErrorView } from './errors';

// Lazy surfaces
export { default as CodeEditor } from './CodeEditor.svelte';
export { default as Sparkline } from './Sparkline.svelte';
export { default as TimeSeriesChart } from './TimeSeriesChart.svelte';
export {
	formatValue,
	gapIntervals,
	latestValue,
	valueExtent,
	formatTimeRange,
	type ChartLine,
	type ValueUnit,
	type TimeRange
} from './timeseries';
export { default as TerminalView } from './TerminalView.svelte';

// Formatting
export * from './format';

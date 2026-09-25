// DockYard's ECharts theme (#22): muted axes, subtle grid, the tooltip as
// a raised surface, tabular numbers. Plain data (no ECharts import), so it
// can be unit-tested; registered by ./echarts.ts.
import { CHART_COLORS as c, EDITOR_COLORS } from './palette';

export const DOCKYARD_ECHARTS_THEME = 'dockyard';

export const dockyardEchartsTheme = {
	color: [...c.series],
	backgroundColor: 'transparent',
	textStyle: { color: c.text, fontFamily: EDITOR_COLORS.fontSans, fontSize: 12 },
	grid: { left: 8, right: 8, top: 16, bottom: 8, containLabel: true },
	categoryAxis: {
		axisLine: { show: true, lineStyle: { color: c.axis } },
		axisTick: { show: false },
		axisLabel: { color: c.text },
		splitLine: { show: false }
	},
	timeAxis: {
		axisLine: { show: true, lineStyle: { color: c.axis } },
		axisTick: { show: false },
		axisLabel: { color: c.text, hideOverlap: true },
		splitLine: { show: false }
	},
	valueAxis: {
		axisLine: { show: false },
		axisTick: { show: false },
		axisLabel: { color: c.text },
		splitLine: { show: true, lineStyle: { color: c.grid } }
	},
	line: {
		symbol: 'none',
		smooth: false,
		lineStyle: { width: 1.75 },
		// Null values are gaps: never connect across them (#5).
		connectNulls: false
	},
	tooltip: {
		backgroundColor: c.tooltipBg,
		borderColor: c.axis,
		borderWidth: 1,
		textStyle: { color: c.textStrong, fontSize: 12 },
		extraCssText: 'border-radius: 6px; box-shadow: 0 12px 32px rgb(0 0 0 / .45);'
	},
	legend: { textStyle: { color: c.text } }
};

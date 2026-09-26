// ECharts, tree-shaken: import only the chart types, components and renderer
// in use (named imports, so the bundler can drop the rest). Loaded through
// import('./echarts') from ./index.ts only; never import it statically.
import { init, registerTheme, use } from 'echarts/core';
import { LineChart } from 'echarts/charts';
import { GridComponent, MarkAreaComponent, TooltipComponent } from 'echarts/components';
import { CanvasRenderer } from 'echarts/renderers';
import { DOCKER_MANAGER_ECHARTS_THEME, dockerManagerEchartsTheme } from './echarts-theme';

// MarkArea shades time ranges without samples (offline intervals, #5).
use([LineChart, GridComponent, TooltipComponent, MarkAreaComponent, CanvasRenderer]);
registerTheme(DOCKER_MANAGER_ECHARTS_THEME, dockerManagerEchartsTheme);

export { init, DOCKER_MANAGER_ECHARTS_THEME };

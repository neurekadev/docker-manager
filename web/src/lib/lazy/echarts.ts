// ECharts, tree-shaken: import only the chart types, components and renderer
// in use (named imports, so the bundler can drop the rest). Loaded through
// import('./echarts') from ./index.ts only; never import it statically.
import { init, registerTheme, use } from 'echarts/core';
import { LineChart } from 'echarts/charts';
import { GridComponent, TooltipComponent } from 'echarts/components';
import { CanvasRenderer } from 'echarts/renderers';
import { DOCKYARD_ECHARTS_THEME, dockyardEchartsTheme } from './echarts-theme';

use([LineChart, GridComponent, TooltipComponent, CanvasRenderer]);
registerTheme(DOCKYARD_ECHARTS_THEME, dockyardEchartsTheme);

export { init, DOCKYARD_ECHARTS_THEME };

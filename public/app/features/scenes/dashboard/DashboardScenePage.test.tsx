import { act, render, screen } from '@testing-library/react';
import React from 'react';
import { TestProvider } from 'test/helpers/TestProvider';
import { getGrafanaContextMock } from 'test/mocks/getGrafanaContextMock';

import { PanelProps } from '@grafana/data';
import { getPanelPlugin } from '@grafana/data/test/__mocks__/pluginMocks';
import { config, locationService, setPluginImportUtils } from '@grafana/runtime';
import { getRouteComponentProps } from 'app/core/navigation/__mocks__/routeProps';

import { DashboardScenePage, Props } from './DashboardScenePage';
import { mockResizeObserver, setupLoadDashboardMock } from './test-utils';

function setup() {
  const context = getGrafanaContextMock();
  const props: Props = {
    ...getRouteComponentProps(),
  };
  props.match.params.uid = 'd10';

  const renderResult = render(
    <TestProvider grafanaContext={context}>
      <DashboardScenePage {...props} />
    </TestProvider>
  );

  return { renderResult, context };
}

const simpleDashboard = {
  title: 'My cool dashboard',
  uid: '10d',
  panels: [
    {
      id: 1,
      type: 'custom-viz-panel',
      title: 'Panel A',
      options: {
        content: `Content A`,
      },
      gridPos: {
        x: 0,
        y: 0,
        w: 10,
        h: 10,
      },
      targets: [],
    },
    {
      id: 2,
      type: 'custom-viz-panel',
      title: 'Panel B',
      options: {
        content: `Content B`,
      },
      gridPos: {
        x: 0,
        y: 10,
        w: 10,
        h: 10,
      },
      targets: [],
    },
  ],
};

const panelPlugin = getPanelPlugin(
  {
    skipDataQuery: true,
  },
  CustomVizPanel
);

config.panels['custom-viz-panel'] = panelPlugin.meta;

setPluginImportUtils({
  importPanelPlugin: (id: string) => Promise.resolve(panelPlugin),
  getPanelPluginFromCache: (id: string) => undefined,
});

mockResizeObserver();

describe('DashboardScenePage', () => {
  beforeEach(() => {
    locationService.push('/');
    setupLoadDashboardMock({ dashboard: simpleDashboard, meta: {} });
    // hacky way because mocking autosizer does not work
    Object.defineProperty(HTMLElement.prototype, 'offsetHeight', { configurable: true, value: 1000 });
    Object.defineProperty(HTMLElement.prototype, 'offsetWidth', { configurable: true, value: 1000 });
  });

  it('Can render dashboard', async () => {
    setup();

    await waitForDashbordToRender();

    expect(await screen.findByTitle('Panel A')).toBeInTheDocument();
    expect(await screen.findByText('Content A')).toBeInTheDocument();

    expect(await screen.findByTitle('Panel B')).toBeInTheDocument();
    expect(await screen.findByText('Content B')).toBeInTheDocument();
  });

  it('Can inspect panel', async () => {
    setup();

    await waitForDashbordToRender();

    expect(screen.queryByText('Inspect: Panel B')).not.toBeInTheDocument();

    act(() => locationService.partial({ inspect: 'panel-2' }));

    expect(await screen.findByText('Inspect: Panel B')).toBeInTheDocument();

    act(() => locationService.partial({ inspect: null }));

    expect(screen.queryByText('Inspect')).not.toBeInTheDocument();
    // Cannot get Menu to show (Looks to be an issue with Dropdown)
    //screen.getByLabelText('Menu for panel with title Panel B').click();
  });

  it('Can view panel in fullscreen', async () => {
    setup();

    await waitForDashbordToRender();

    expect(await screen.findByTitle('Panel A')).toBeInTheDocument();

    act(() => locationService.partial({ viewPanel: 'panel-2' }));

    expect(screen.queryByTitle('Panel A')).not.toBeInTheDocument();
    expect(await screen.findByTitle('Panel B')).toBeInTheDocument();
  });
});

interface VizOptions {
  content: string;
}
interface VizProps extends PanelProps<VizOptions> {}

function CustomVizPanel(props: VizProps) {
  return <div>{props.options.content}</div>;
}

async function waitForDashbordToRender() {
  expect(await screen.findByText('Last 6 hours')).toBeInTheDocument();
  //act(() => resizeObserverMock.callResizeObserverListeners(500, 500));
}

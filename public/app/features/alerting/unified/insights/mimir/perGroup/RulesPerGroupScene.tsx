import React from 'react';

import { PanelBuilders, SceneFlexItem, SceneQueryRunner } from '@grafana/scenes';
import { BigValueGraphMode, DataSourceRef } from '@grafana/schema';

import { PANEL_STYLES } from '../../../home/Insights';
import { InsightsRatingModal } from '../../RatingModal';

export function getRulesPerGroupScene(datasource: DataSourceRef, panelTitle: string) {
  const query = new SceneQueryRunner({
    datasource,
    queries: [
      {
        refId: 'A',
        expr: `sum(grafanacloud_instance_rule_group_rules{rule_group="$rule_group"})`,
        range: true,
        legendFormat: 'number of rules',
      },
    ],
  });

  return new SceneFlexItem({
    ...PANEL_STYLES,
    body: PanelBuilders.stat()
      .setTitle(panelTitle)
      .setDescription(panelTitle)
      .setData(query)
      .setUnit('none')
      .setOption('graphMode', BigValueGraphMode.Area)
      .setOverrides((b) =>
        b.matchFieldsByQuery('A').overrideColor({
          mode: 'fixed',
          fixedColor: 'blue',
        })
      )
      .setNoValue('0')
      .setHeaderActions(<InsightsRatingModal panel={panelTitle} />)
      .build(),
  });
}

import React from 'react';

import { config } from '@grafana/runtime';
import { Button } from '@grafana/ui';

import { CollapsableAlert } from './CollapsableAlert';

const LOCAL_STORAGE_KEY = 'grafana.unifiedalerting.upgrade.previewNotice';

export const UAPreviewNotice = () => {
  if (config.unifiedAlertingEnabled || !config.featureToggles.alertingPreviewUA) {
    return null;
  }

  return <CollapsableAlert
            localStoreKey={LOCAL_STORAGE_KEY}
            alertTitle={"This is a preview of Grafana Alerting"}
            collapseText={"Grafana Alerting Preview"}
            collapseTooltip={"Show preview warning"}
            severity={"warning"}
  >
    <p>
      No rules are being evaluated and legacy alerting is still running.
      <br />
      Please contact your administrator to upgrade permanently.
    </p>
    <Button fill="text" icon="book" size="sm">
      <a href="https://grafana.com/docs/grafana/latest/alerting/set-up/migrating-alerts/">
        Read documentation
      </a>
    </Button>
  </CollapsableAlert>
}

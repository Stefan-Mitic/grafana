import React from 'react';

import { config } from '@grafana/runtime';
import { Alert, Button } from '@grafana/ui';

export const LOCAL_STORAGE_KEY = 'grafana.legacyalerting.unifiedalertingpreview';

const UAPreviewNotice = () => (
  <>
    {(!config.unifiedAlertingEnabled && config.featureToggles.alertingPreviewUA) ? (
      <Alert severity="warning" title="This is a preview of Grafana Alerting.">
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
      </Alert>
    ) : null}
  </>
);

export { UAPreviewNotice };

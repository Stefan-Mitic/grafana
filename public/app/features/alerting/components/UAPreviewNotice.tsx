import { css } from '@emotion/css';
import React from 'react';
import { useLocalStorage } from 'react-use';

import { GrafanaTheme2 } from '@grafana/data';
import { config } from '@grafana/runtime';
import {Alert, Button, HorizontalGroup, Tooltip, useStyles2} from '@grafana/ui';

const LOCAL_STORAGE_KEY = 'grafana.unifiedalerting.upgrade.previewNotice';

const UAPreviewNotice = () => {
  const styles = useStyles2(getStyles);
  const [closed, setClosed] = useLocalStorage(LOCAL_STORAGE_KEY, false);
  if (config.unifiedAlertingEnabled || !config.featureToggles.alertingPreviewUA) {
    return null;
  }

  return (
    <>
      {closed && (
        <HorizontalGroup height="auto" justify="flex-end">
          <Tooltip content="Show preview warning" placement="bottom">
            <Button fill="text" variant="secondary" icon="exclamation-triangle" className={styles.warningButton} onClick={() => setClosed(false)}>
              {"Grafana Alerting Preview"}
            </Button>
          </Tooltip>
        </HorizontalGroup>
      )}
      {!closed && (
        <Alert severity="warning" title="This is a preview of Grafana Alerting." onRemove={() => setClosed(true)}>
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
      )}
    </>
  );
}

const getStyles = (theme: GrafanaTheme2) => {
  const color = theme.colors['warning'];
    return {
      warningButton: css`
        color: ${color.text};

        &:hover {
          background: ${color.transparent};
        }
      `,
    };
}

export { UAPreviewNotice };

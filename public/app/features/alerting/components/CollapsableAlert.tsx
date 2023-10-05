import { css } from '@emotion/css';
import React, {HTMLAttributes} from 'react';
import { useLocalStorage } from 'react-use';

import { GrafanaTheme2 } from '@grafana/data';
import {Alert, AlertVariant, Button, Tooltip, useTheme2} from '@grafana/ui';
import {getIconFromSeverity} from '@grafana/ui/src/components/Alert/Alert';
import {Flex, JustifyContent} from '@grafana/ui/src/components/Flex/Flex';

interface CollapsableAlertProps extends HTMLAttributes<HTMLDivElement> {
  localStoreKey: string;
  startClosed?: boolean;
  severity?: AlertVariant;
  collapseText?: string;
  collapseTooltip: string;
  justifyContent?: JustifyContent;
  alertTitle: string;
  children?: React.ReactNode;
}


export const CollapsableAlert = ({localStoreKey, startClosed = false, severity = "error", collapseText, collapseTooltip, justifyContent="flex-end", alertTitle, children}: CollapsableAlertProps) => {
  const theme = useTheme2();
  const styles = getStyles(theme, severity);
  const [closed, setClosed] = useLocalStorage(localStoreKey, startClosed);

  return (
    <Flex justifyContent={justifyContent}>
      {closed && (
        <Tooltip content={collapseTooltip} placement="bottom">
          <Button fill="text" variant="secondary" icon={getIconFromSeverity(severity)} className={styles.warningButton} onClick={() => setClosed(false)}>
            {collapseText}
          </Button>
        </Tooltip>
      )}
      {!closed && (
        <Alert severity={severity} title={alertTitle} onRemove={() => setClosed(true)}>
          {children}
        </Alert>
      )}
    </Flex>
  );
}

const getStyles = (theme: GrafanaTheme2,  severity: AlertVariant,) => {
  const color = theme.colors[severity];
    return {
      warningButton: css`
        color: ${color.text};

        &:hover {
          background: ${color.transparent};
        }
      `,
    };
}

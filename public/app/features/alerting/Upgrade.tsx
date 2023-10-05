import { css, cx } from '@emotion/css';
import uFuzzy from '@leeoniya/ufuzzy';
import { createSelector } from '@reduxjs/toolkit';
import {debounce, uniq} from 'lodash';
import pluralize from 'pluralize';
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import {useLocation} from 'react-router-dom';
import { useLocalStorage } from 'react-use';

import { GrafanaTheme2, UrlQueryMap } from '@grafana/data';
import { selectors } from '@grafana/e2e-selectors';
import { Stack } from '@grafana/experimental';
import {locationService} from '@grafana/runtime';
import {
  Alert,
  Badge,
  Button,
  CallToActionCard,
  ConfirmModal,
  FilterInput,
  HorizontalGroup,
  Icon,
  Link,
  Tab,
  TabContent,
  TabsBar,
  TagList,
  Text,
  Tooltip,
  useStyles2
} from '@grafana/ui';
import { Page } from 'app/core/components/Page/Page';
import {useQueryParams} from 'app/core/hooks/useQueryParams';

import PageLoader from '../../core/components/PageLoader/PageLoader';
import {MatcherOperator} from '../../plugins/datasource/alertmanager/types';
import {getSearchPlaceholder} from '../search/tempI18nPhrases';

import {AlertPair, ContactPair, DashboardUpgrade, OrgMigrationSummary, upgradeApi} from "./unified/api/upgradeApi";
import {DynamicTable, DynamicTableColumnProps, DynamicTableItemProps} from "./unified/components/DynamicTable";
import {DynamicTableWithGuidelines} from "./unified/components/DynamicTableWithGuidelines";
import {ProvisioningBadge} from "./unified/components/Provisioning";
import {Matchers} from './unified/components/notification-policies/Matchers';
import {ActionIcon} from './unified/components/rules/ActionIcon';
import {getPaginationStyles} from './unified/styles/pagination';
import {
  createContactPointLink,
  makeDashboardLink,
  makeFolderLink
} from './unified/utils/misc';
import {createUrl} from "./unified/utils/url";


export const UpgradePage = () => {
  const { useGetOrgUpgradeSummaryQuery } = upgradeApi;
  const {
    currentData: summary,
    isFetching: isFetching,
    isError: isFetchError,
    error: fetchError,
  } = useGetOrgUpgradeSummaryQuery();

  const alertCount = (summary?.migratedDashboards ?? []).reduce((acc, cur) => acc + (cur?.migratedAlerts?.length ?? 0), 0);
  const contactCount = summary?.migratedChannels?.length ?? 0;

  const errors = summary?.errors ?? [];
  const hasData = alertCount > 0 || contactCount > 0 || errors.length > 0

  return (
      <Page navId="alerting-upgrade">
          <Page.Contents>
            {isFetchError && (
              <Alert severity="error" title="Error loading Grafana Alerting upgrade information">
                {fetchError instanceof Error ? fetchError.message : 'Unknown error.'}
              </Alert>
            )}
            {!isFetchError && !isFetching && !hasData && (
              <CTAElement/>
            )}
            {!isFetchError && hasData && (
              <>
                <ErrorSummary errors={errors}/>
                <UpgradeTabs
                  alertCount={alertCount}
                  contactCount={contactCount}
                />
              </>
            )}
          </Page.Contents>
      </Page>
  );
};

interface UpgradeTabsProps {
  alertCount: number;
  contactCount: number;
}

export const UpgradeTabs = ({alertCount, contactCount}: UpgradeTabsProps) => {
  const styles = useStyles2(getStyles);
  const { useCancelOrgUpgradeMutation } = upgradeApi;

  const [startOver, {isLoading}] = useCancelOrgUpgradeMutation();

  const [queryParams, setQueryParams] = useQueryParams();
  const { tab } = getActiveTabFromUrl(queryParams);

  const [activeTab, setActiveTab] = useState<ActiveTab>(tab);

  const [showConfirmStartOver, setShowConfirmStartOver] = useState(false);


  const cancelUpgrade = async () => {
    await startOver()
    setShowConfirmStartOver(false);
  }

  if (isLoading) {
    return <PageLoader/>
  }

  return (
          <>
            <TabsBar>
              <Tab
                label={"Upgraded Alerts"}
                active={activeTab === ActiveTab.Alerts}
                counter={alertCount}
                icon={"bell"}
                onChangeTab={() => {
                  setActiveTab(ActiveTab.Alerts);
                  setQueryParams({ tab: ActiveTab.Alerts });
                }}
              />
              <Tab
                label={"Upgraded Contacts"}
                active={activeTab === ActiveTab.Contacts}
                counter={contactCount}
                icon={"at"}
                onChangeTab={() => {
                  setActiveTab(ActiveTab.Contacts);
                  setQueryParams({ tab: ActiveTab.Contacts });
                }}
              />
              <HorizontalGroup height="auto" justify="flex-end">
                <Button
                  size="sm"
                  variant="destructive"
                  onClick={() => setShowConfirmStartOver(true)}
                  icon={"trash-alt"}
                  className={""}
                >
                  {"Cancel Upgrade"}
                </Button>
                {showConfirmStartOver && (
                  <ConfirmModal
                    isOpen={true}
                    title="Cancel Upgrade Process"
                    body={
                      <Stack direction="column" gap={0.5}>
                        <Text color="primary">Are you sure you want to cancel your upgrade?</Text>
                        <Text color="secondary" variant="bodySmall">All Grafana Alerting resources will be deleted. This includes: alert rules, contact points, notification policies, silences, and mute timings.</Text>
                        <Text color="secondary" variant="bodySmall" weight="bold">No legacy alerts or notification channels will be affected.</Text>
                      </Stack>
                    }
                    confirmText="Yes, delete Grafana Alerting resources"
                    onConfirm={cancelUpgrade}
                    onDismiss={() => setShowConfirmStartOver(false)}
                  />
                )}
              </HorizontalGroup>
            </TabsBar>
            <TabContent className={styles.tabContent}>
              <>
                {activeTab === ActiveTab.Alerts && (
                  <AlertTabContentWrapper/>
                )}
                {activeTab === ActiveTab.Contacts && (
                  <ChannelTabContentWrapper/>
                )}
              </>

            </TabContent>
          </>
  );
};

enum ActiveTab {
  Alerts = 'alerts',
  Contacts = 'contacts',
}

interface QueryParamValues {
  tab: ActiveTab;
}

function getActiveTabFromUrl(queryParams: UrlQueryMap): QueryParamValues {
  let tab = ActiveTab.Alerts; // default tab

  if (queryParams['tab'] === ActiveTab.Alerts) {
    tab = ActiveTab.Alerts;
  }

  if (queryParams['tab'] === ActiveTab.Contacts) {
    tab = ActiveTab.Contacts;
  }

  return {
    tab,
  };
}

const CTAElement = () => {
  const { useUpgradeOrgMutation } = upgradeApi;
  const [startUpgrade, {isLoading}] = useUpgradeOrgMutation();

  const upgradeAlerting = async () => {
    await startUpgrade()
  }

  if (isLoading) {
    return <PageLoader/>
  }

  const footer = (
    <>
      <span key="proTipFooter">
        <p><Icon name="rocket" />{' '} Note: {"Starting the upgrade process will not affect your existing legacy alerts."}</p>
        <p>{"For more information, please refer to the "}<Button fill="text" icon="book" size="sm">
      <a href="https://grafana.com/docs/grafana/latest/alerting/set-up/migrating-alerts/">
      Grafana Alerting Migration Guide
      </a>
    </Button></p>

        {/* <div className={css({'align-items': 'left'})}>
        <p>{"This process will automatically copy and convert your existing legacy alerts to Grafana Alerting, however Grafana Alerting will not be enabled until you modify the Grafana configuration." +
          "This means the newly created Alerts, Contact Points, and Notification Policies will be available to preview or even modify, but will not run or send any notifications."}</p>
        <p>{"The upgrade can be performed as many times as you wish in case new legacy alerts have been created since the previous run. " +
          "Note that when refreshing the upgrade will lose, any manual changes to Grafana Alerting resources will be lost."}</p>
        <p><Icon name="exclamation-triangle" />{' '} Note: {"This process might create new folders if doing so is necessary to retain correct permissions for new alerts."}</p>
        <br/>
        </div> */}
      </span>
    </>
  );

  const cta = (
    <div>
      <Stack direction="column" gap={1}>
        <Stack direction="row" gap={1}>
          <Button
            size="lg"
            variant="destructive"
            onClick={upgradeAlerting}
            icon={"bell"}
            className={""}
            data-testid={selectors.components.CallToActionCard.buttonV2("Start Upgrade")}
          >
            {"Start Upgrade"}
          </Button>
        </Stack>
      </Stack>
    </div>
  );

  const ctaStyle = css`
    text-align: center;
  `;

  return <CallToActionCard className={ctaStyle}
                           message={"Start the upgrade to Grafana Alerting."}
                           footer={footer}
                           callToActionElement={cta}/>;
}

const AlertTabContentWrapper = () => {
  const columns = useAlertColumns();
  const filterParam = "alertFilter";
  const [queryParam] = useSingleQueryParam(filterParam);

  const selectRows = useMemo(() => {
    const emptyArray: Array<DynamicTableItemProps<DashboardUpgrade>> = [];
    return createSelector(
      (res: OrgMigrationSummary | undefined) => res?.migratedDashboards ?? [],
      (rows) => rows ?? emptyArray,
    )}, [])

  const { items } = upgradeApi.useGetOrgUpgradeSummaryQuery(undefined, {
    selectFromResult: ({ data }) => ({
      items: selectRows(data),
    }),
  })

  const searchSpaceMap = useCallback((dashUpgrade: DashboardUpgrade) => `${dashUpgrade.folderName} ${dashUpgrade.dashboardName} ${dashUpgrade.newFolderName}`, []);
  const renderExpandedContent = useCallback(({data: dashUpgrade}: {data: DashboardUpgrade}) => <AlertTable dashboardUid={dashUpgrade.dashboardUid ?? ''}
                                                                                 dashboardId={dashUpgrade.dashboardId}
                                                                                 showGuidelines={true}/>, []);

  return <AlertTabContent
    rows={items}
    filterParam={filterParam}
    queryParam={queryParam}
    searchSpaceMap={searchSpaceMap}
    emptyMessage={"No alert upgrades found."}
    searchPlaceholder={getSearchPlaceholder(false)}
    columns={columns}
    isExpandable={true}
    renderExpandedContent={renderExpandedContent}
  />
}
AlertTabContentWrapper.displayName = "AlertTabContentWrapper";

const ChannelTabContentWrapper = () => {
  const columns = useChannelColumns();

  const filterParam = "contactFilter";
  const [queryParam] = useSingleQueryParam(filterParam);

  const selectRows = useMemo(() => {
    const emptyArray: Array<DynamicTableItemProps<ContactPair>> = [];
    return createSelector(
      (res: OrgMigrationSummary | undefined) => res?.migratedChannels ?? [],
      (rows) => rows ?? emptyArray,
    )}, [])

  const { items } = upgradeApi.useGetOrgUpgradeSummaryQuery(undefined, {
    selectFromResult: ({ data }) => ({
      items: selectRows(data),
    }),
  })

  const searchSpaceMap = useCallback((pair: ContactPair) => `${pair.legacyChannel?.name} ${pair.contactPoint?.name} ${pair.legacyChannel?.type}`, []);

  return <ChannelTabContent
    rows={items}
    filterParam={filterParam}
    queryParam={queryParam}
    searchSpaceMap={searchSpaceMap}
    emptyMessage={"No channel upgrades found."}
    searchPlaceholder={"Search for channel and contact point names"}
    columns={columns}
  />
}
ChannelTabContentWrapper.displayName = "ChannelTabContentWrapper";

function useSingleQueryParam(name: string): [string | undefined, (values: string) => void] {
  const { search } = useLocation();
  const param =  useMemo(() => {
    return new URLSearchParams(search).get(name) || '';
  }, [name, search]);
  // const param = useMemo(() => queryParams[name] === undefined ? undefined : String(queryParams[name]), [queryParams, name]);
  const update = useCallback((value: string) => {
    return locationService.partial({ [name]: value || null })
  }, [name]);
  return [param, update];
}

interface UpgradeTabContentProps<T extends object> {
  rows?: T[];
  filterParam: string;
  queryParam?: string;
  searchSpaceMap: (row: T) => string;
  columns: Array<DynamicTableColumnProps<T>>
  isExpandable?: boolean;
  renderExpandedContent?: (item: DynamicTableItemProps<T>) => React.ReactNode;
  emptyMessage: string;
  searchPlaceholder: string;
}

const UpgradeTabContent =  <T extends object,>({
                                 rows = [],
                                 filterParam,
                                 queryParam,
                                 searchSpaceMap,
                                 columns,
                                 isExpandable=false,
                                 renderExpandedContent,
                                 emptyMessage,
                                 searchPlaceholder,
                      }: UpgradeTabContentProps<T>) => {
  const styles = useStyles2(getStyles);

  const [filter, setFilter] = useState(queryParam || '');

  const filterFn = useMemo(() => {
    return createfilterByMapping<T>(searchSpaceMap);
  }, [searchSpaceMap]);

  const items = useMemo((): Array<DynamicTableItemProps<T>> => {
    return filterFn(rows, filter).map((row, Idx) => {
      return {
        id: `${searchSpaceMap(row)} - ${Idx}`,
        data: row,
      };
    });
  }, [searchSpaceMap, filterFn, rows, filter]);

  const showGuidelines = false;
  const wrapperClass = cx(styles.wrapper, { [styles.wrapperMargin]: showGuidelines });

  const TableComponent = showGuidelines ? DynamicTableWithGuidelines : DynamicTable;

  const pagination = useMemo(() => ({ itemsPerPage: 10 }), []);

  return (
    <>
      <div className={styles.searchWrapper}>
        <Stack direction="column" gap={1}>
          <Stack direction="row" gap={1}>
            <Search
              placeholder={searchPlaceholder}
              searchFn={(phrase) => {
                setFilter(phrase || '');
                locationService.partial({ [filterParam]: phrase || null })
              }}
              searchPhrase={filter || ''}
            />
          </Stack>
        </Stack>
      </div>

      {!!items.length && (<div className={wrapperClass}>
          <TableComponent
            cols={columns}
            isExpandable={isExpandable}
            items={items}
            renderExpandedContent={renderExpandedContent}
            pagination={pagination}
            paginationStyles={styles.pagination}
          />
        </div>
      )}
      {!items.length && (
        <div className={cx(wrapperClass, styles.emptyMessage)}>{emptyMessage}</div>
      )}
    </>
  );
};

const ChannelTabContent = React.memo(UpgradeTabContent<ContactPair>);
const AlertTabContent = React.memo(UpgradeTabContent<DashboardUpgrade>);

const useChannelColumns = (): Array<DynamicTableColumnProps<ContactPair>> => {
  const styles = useStyles2(getStyles);
  return useMemo(() => (
    [
      {
        id: 'legacyChannel',
        label: 'Legacy Channel',
        // eslint-disable-next-line react/display-name
        renderCell: ({ data: contactPair }) => {
          if (!contactPair?.legacyChannel) {
            return null;
          }
          return (
            <Stack direction={"row"} gap={1}>
              <Link rel="noreferrer"
                    target="_blank"
                    className={styles.textLink}
                    href={createUrl(`/alerting-legacy/notifications/receivers/${encodeURIComponent(contactPair.legacyChannel.id)}/edit`, {})}>
                {contactPair.legacyChannel.name}
              </Link>
              { contactPair.legacyChannel?.type && (<Badge color="blue" text={contactPair.legacyChannel.type} />)}
            </Stack>
          )
        },
        size: 5,
      },
      {
        id: 'arrow',
        label: '',
        renderCell: ({ data: contactPair }) => {
          if (!contactPair?.contactPoint) {
            return null;
          }
          return <Icon name="arrow-right" />;
        },
        size: '45px',
      },
      {
        id: 'route',
        label: 'Notification Policy',
        renderCell: ({ data: contactPair }) => {
          return (<>
            {contactPair?.contactPoint && (
              <Matchers matchers={[[`__contacts_${contactPair.contactPoint.uid}__`, MatcherOperator.equal, "true"]]} />
            )}
          </>)
        },
        size: 5,
      },
      {
        id: 'arrow2',
        label: '',
        renderCell: ({ data: contactPair }) => {
          if (!contactPair?.contactPoint) {
            return null;
          }
          return <Icon name="arrow-right" />;
        },
        size: '45px',
      },
      {
        id: 'contactPoint',
        label: 'Contact Point',
        // eslint-disable-next-line react/display-name
        renderCell: ({ data: contactPair }) => {
          return (
            <Stack direction={"row"} gap={1}>
              {contactPair?.contactPoint && (
                <><Link rel="noreferrer"
                        target="_blank"
                        className={styles.textLink}
                        href={createContactPointLink(contactPair.contactPoint.name, 'grafana')}>
                  {contactPair.contactPoint.name}
                </Link>
                  <Badge color="blue" text={contactPair.contactPoint.type} /></>
              )}
              {contactPair.error && (
                <Tooltip theme="error" content={contactPair.error}>
                  <Icon name="exclamation-triangle" className={styles.warningIcon} size={"lg"}/>
                </Tooltip>
              )}
            </Stack>
          )
        },
        size: 5,
      },
      {
        id: 'provisioned',
        label: '',
        renderCell: ({ data: contactPair }) => {
          return contactPair.provisioned ? <ProvisioningBadge /> : null;
        },
        size: '100px',
      },
    ]
    ), [styles.textLink, styles.warningIcon]);
}

const useAlertColumns = (): Array<DynamicTableColumnProps<DashboardUpgrade>> => {
  const styles = useStyles2(getStyles);

  const { useUpgradeDashboardMutation } = upgradeApi;
  const [migrateDashboard] = useUpgradeDashboardMutation();

  return useMemo(() => ([
    {
      id: 'dashboard-level-error',
      label: '',
      renderCell: ({ data: dashUpgrade }) => {
        const error = (dashUpgrade.errors ?? []).join('\n');
        if (!error) {
          return null;
        }
        return (
          <Tooltip theme="error" content={error}>
            <Icon name="exclamation-triangle" className={styles.warningIcon} size={"lg"}/>
          </Tooltip>
        )
      },
      size: '45px',
    },
    {
      id: 'folder',
      label: 'Folder',
      renderCell: ({ data: dashUpgrade }) => {
        if (!dashUpgrade.folderName) {
          return <Stack alignItems={"center"} gap={0.5}><Icon name="folder" /><Badge color="red" text="Unknown Folder"/></Stack>;
        }
        return <Stack alignItems={"center"} gap={0.5}><Icon name="folder" />{dashUpgrade.folderName}</Stack>
      },
      size: 2,
    },
    {
      id: 'dashboard',
      label: 'Dashboard',
      renderCell: ({ data: dashUpgrade }) => {
        if (!dashUpgrade.dashboardName) {
          return <Stack alignItems={"center"} gap={0.5}><Icon name="apps" /><Badge color="red" text={`Unknown Dashboard ID:${dashUpgrade.dashboardId}`}/></Stack>
        }
        return <Stack alignItems={"center"} gap={0.5}><Icon name="apps" />{dashUpgrade.dashboardName}</Stack>
      },
      size: 2,
    },
    {
      id: 'new-folder-arrow',
      label: '',
      renderCell: ({ data: dashUpgrade }) => {
        const migratedFolderUid = dashUpgrade?.newFolderUid;
        const folderChanged = migratedFolderUid!! && migratedFolderUid !== dashUpgrade.folderUid;
        if (folderChanged && dashUpgrade?.newFolderName) {
          return <Icon name="arrow-right" />;
        }
        return null;
      },
      size: '45px',
    },
    {
      id: 'new-folder',
      label: 'New Folder',
      renderCell: ({ data: dashUpgrade }) => {
        const migratedFolderUid = dashUpgrade?.newFolderUid;
        const folderChanged = migratedFolderUid!! && migratedFolderUid !== dashUpgrade.folderUid;
        if (folderChanged && dashUpgrade?.newFolderName) {
          const newFolderWarning = dashUpgrade.warnings.find((warning) => warning.includes('dashboard alerts moved'));
          return <Stack alignItems={"center"} gap={0.5}>
                  <Icon name={"folder"} />
                  {dashUpgrade?.newFolderName}
                  {newFolderWarning && (
                    <Tooltip theme="info-alt" content={newFolderWarning} placement="top">
                      <Icon name={'info-circle'}/>
                    </Tooltip>
                  )}
                </Stack>;
        }
        return null;
      },
      size: 3,
    },
    {
      id: 'provisioned',
      label: '',
      className: styles.tableBadges,
      renderCell: ({ data: dashUpgrade }) => {
        const provisionedWarning = dashUpgrade.warnings.find((warning) => warning.includes('failed to get provisioned status'));
        const badge = css({
          width: '100%',
          justifyContent: 'center',
        });
        return (
          <>
            {dashUpgrade.provisioned && (
              <Badge color="purple" text={provisionedWarning ? "Unknown" : "Provisioned"} tooltip={provisionedWarning} icon={provisionedWarning ? "exclamation-triangle" : undefined} className={badge}/>
            )}
          </>
        )
      },
      size: '100px',
    },
    {
      id: 'badges',
      label: '',
      className: styles.tableBadges,
      renderCell: ({ data: dashUpgrade }) => {
        const migratedAlerts = dashUpgrade?.migratedAlerts ?? [];
        const nestedErrors = migratedAlerts.map((alertPair) => alertPair.error ?? '').filter((error) => !!error);
        const badge = css({
          minWidth: '60px',
          justifyContent: 'center',
        });
        return (
          <Stack gap={0.5}>
            {nestedErrors.length > 0 && (<Badge color="red" key="errors" text={`${nestedErrors.length} errors`} tooltip={uniq(nestedErrors).join('\n')} className={badge}/>)}
            <Badge color="green" key="alerts" text={`${migratedAlerts.length} alerts`} className={badge}/>
          </Stack>
        )
      },
      size: '142px',
    },
    {
      id: 'actions',
      label: 'Actions',
      renderCell: ({ data: dashUpgrade }) => {
        const migratedFolderUid = dashUpgrade?.newFolderUid;
        const folderChanged = migratedFolderUid!! && migratedFolderUid !== dashUpgrade.folderUid;
        return (
          <Stack gap={0.5} alignItems="center">
            {dashUpgrade.dashboardId && (<ActionIcon
              // className={styles.destructiveActionIcon}
              aria-label="re-upgrade legacy alerts for this dashboard"
              key="upgrade-dashboard"
              icon="sync"
              tooltip="re-upgrade legacy alerts for this dashboard"
              onClick={() => migrateDashboard({dashboardId: dashUpgrade.dashboardId})}
            />)}
            {dashUpgrade.folderUid && (<ActionIcon
              aria-label="go to folder"
              key="gotofolder"
              icon="folder-open"
              tooltip="go to folder"
              to={makeFolderLink(dashUpgrade.folderUid)}
              target="__blank"
            />)}
            {dashUpgrade.dashboardUid && (<ActionIcon
              aria-label="go to dashboard"
              key="gotodash"
              icon="apps"
              tooltip="go to dashboard"
              to={makeDashboardLink(dashUpgrade.dashboardUid)}
              target="__blank"
            />)}
            {folderChanged && migratedFolderUid && (
              <>
                <Icon name="arrow-right"/>
                <ActionIcon
                  aria-label="go to new folder"
                  key="gotonew"
                  icon="folder-open"
                  tooltip="go to new folder"
                  to={makeFolderLink(migratedFolderUid)}
                  target="__blank"/>
              </>)}
          </Stack>
        )
      },
      size: '150px',
    },
  ]), [styles.tableBadges, styles.warningIcon, migrateDashboard]);
}

const ufuzzy = new uFuzzy({
  intraMode: 1,
  intraIns: 1,
  intraSub: 1,
  intraTrn: 1,
  intraDel: 1,
});

const createfilterByMapping = <T,>(searchSpaceMap: (row: T) => string) => {
  return (filterables:  T[], filter: string | undefined) => {
    if (!filter) {
      return filterables;
    }
    const haystack = filterables.map(searchSpaceMap);

    const [idxs, info, order] = ufuzzy.search(haystack, filter);
    if (info && order) {
      return order.map((idx) => filterables[info.idx[idx]]);
    } else if (idxs) {
      return idxs.map((idx) => filterables[idx]);
    }

    return filterables;
  }
};

interface SearchProps {
  searchFn: (searchPhrase: string) => void;
  searchPhrase: string | undefined;
  placeholder?: string;
}

const Search = ({ searchFn, searchPhrase, placeholder }: SearchProps) => {
  const [searchFilter, setSearchFilter] = useState(searchPhrase);

  const debouncedSearch = useMemo(() => debounce(searchFn, 600), [searchFn]);

  useEffect(() => {
    return () => {
      // Stop the invocation of the debounced function after unmounting
      debouncedSearch?.cancel();
    };
  }, [debouncedSearch]);

  return (
    <FilterInput
      placeholder={placeholder}
      value={searchFilter}
      width={55}
      escapeRegex={false}
      onChange={(value) => {
        setSearchFilter(value || '');
        if (value === '') {
          // This is so clicking clear is instant. Otherwise, clearing and switching tabs before debounce is ready will lose filter state.
          debouncedSearch?.cancel();
          searchFn('');
        } else {
          debouncedSearch(value || '');
        }
      }}
    />
  );
};

interface AlertTableProps {
  dashboardId: number;
  dashboardUid: string;
  showGuidelines?: boolean;
  emptyMessage?: string;
}

const AlertTable = ({
                      dashboardId,
                      dashboardUid,
                        showGuidelines = false,
                        emptyMessage = 'No alert upgrades found.',
                      }: AlertTableProps) => {
  const styles = useStyles2(getStyles);

  const selectRowsForDashUpgrade = useMemo(() => {
    const emptyArray: Array<DynamicTableItemProps<AlertPair>> = [];
    return createSelector(
      (res: OrgMigrationSummary | undefined) => res?.migratedDashboards ?? [],
      (res: OrgMigrationSummary | undefined, dashboardId: number) => dashboardId,
      (migratedDashboards, dashboardId) => migratedDashboards?.find((du) => du.dashboardId === dashboardId)?.migratedAlerts.map((alertPair, Idx) => {
        return {
          id: `${alertPair?.legacyAlert?.id}-${Idx}`,
          data: alertPair,
        };
      }) ?? emptyArray,
    )}, [])

  const { items } = upgradeApi.useGetOrgUpgradeSummaryQuery(undefined, {
    selectFromResult: ({ data }) => ({
      items: selectRowsForDashUpgrade(data, dashboardId),
    }),
  })

  const { useUpgradeAlertMutation } = upgradeApi;
  const [migrateAlert] = useUpgradeAlertMutation();

  const wrapperClass = cx(styles.wrapper, styles.rulesTable, { [styles.wrapperMargin]: showGuidelines });

  const columns: Array<DynamicTableColumnProps<AlertPair>> = [
    {
      id: 'legacyAlert',
      label: 'Legacy Alert',
      renderCell: ({ data: alertPair }) => {
        if (!alertPair?.legacyAlert) {
          return null;
        }
        return (<>
          { dashboardUid ? (<Link rel="noreferrer"
                target="_blank"
                className={styles.textLink}
                href={createUrl(`/d/${encodeURIComponent(dashboardUid)}`,
                  {
                    editPanel: String(alertPair.legacyAlert.panelId),
                    tab: "alert",
                  })}>
            {alertPair.legacyAlert.name}
          </Link>) : (<Badge color="red" text={alertPair.legacyAlert.name}/>)}
          </>
        )
      },
      size: 5,
    },
    {
      id: 'arrow',
      label: '',
      renderCell: ({ data: alertPair }) => {
        if (!alertPair?.legacyAlert) {
          return null;
        }
        return <Icon name="arrow-right" />;
      },
      size: '45px',
    },
    {
      id: 'alertRule',
      label: 'Alert Rule',
      renderCell: ({ data: alertPair }) => {
        return (
        <Stack direction={"row"} gap={1}>
          {alertPair?.alertRule && (
            <Link rel="noreferrer"
                    target="_blank"
                    className={styles.textLink}
                    href={createUrl(`/alerting/grafana/${alertPair.alertRule?.uid??''}/view`, {})}>
              {alertPair.alertRule?.title??''}
            </Link>
          )}
          {alertPair.error && (
            <Tooltip theme="error" content={alertPair.error}>
              <Icon name="exclamation-triangle" className={styles.warningIcon} size={"lg"}/>
            </Tooltip>
          )}
        </Stack>
        )
      },
      size: 5,
    },
    {
      id: 'contacts',
      label: 'Sends To',
      renderCell: ({ data: alertPair }) => {
        // TODO: Maybe even show the routing preview.
        return (<>
          {!alertPair.error && (<TagList tags={alertPair?.alertRule?.sendsTo ?? []} displayMax={3} className={css({justifyContent: 'flex-start', width:'100%'})}/>)}
        </>
        );
      },
      size: 3,
    },
    {
      id: 'actions',
      label: 'Actions',
      renderCell: ({ data: alertPair }) => {
        if (!alertPair?.legacyAlert) {
          return null;
        }
        if (alertPair.legacyAlert.dashboardId <= 0 || alertPair.legacyAlert.panelId <= 0) {
          return null;
        }
        return (
          <Stack gap={0.5} alignItems="center">
            <ActionIcon
              // className={styles.destructiveActionIcon}
              aria-label="re-upgrade legacy alert"
              key="upgrade-alert"
              icon="sync"
              tooltip="re-upgrade legacy alert"
              onClick={() => migrateAlert({dashboardId: alertPair.legacyAlert.dashboardId, panelId: alertPair.legacyAlert.panelId})}
            />
          </Stack>
        )
      },
      size: '70px',
    },
  ];

  if (!items.length) {
    return <div className={cx(wrapperClass, styles.emptyMessage)}>{emptyMessage}</div>;
  }

  const TableComponent = showGuidelines ? DynamicTableWithGuidelines : DynamicTable;

  return (
    <div className={wrapperClass} data-testid="rules-table">
      <TableComponent
        cols={columns}
        // isExpandable={true}
        items={items}
        // renderExpandedContent={({ data: rule }) => <RuleDetails rule={rule} />}
        pagination={{ itemsPerPage: 10 }}
        paginationStyles={styles.pagination}
      />
    </div>
  );
};

interface ErrorSummaryButtonProps {
  count: number;
  onClick: () => void;
}

const ErrorSummaryButton = ({ count, onClick }: ErrorSummaryButtonProps) => {
  return (
    <HorizontalGroup height="auto" justify="flex-start">
      <Tooltip content="Show all errors" placement="top">
        <Button fill="text" variant="destructive" icon="exclamation-triangle" onClick={onClick}>
          {count > 1 ? <>{count} errors</> : <>1 error</>}
        </Button>
      </Tooltip>
    </HorizontalGroup>
  );
};

interface ErrorSummaryProps {
  errors: string[];
}

const ErrorSummary = ({ errors }: ErrorSummaryProps) => {
  const [expanded, setExpanded] = useState(false);
  const [closed, setClosed] = useLocalStorage('grafana.unifiedalerting.upgrade.hideErrors', true);
  const styles = useStyles2(getStyles);

  return (
    <>
      {!!errors.length && closed && (
        <ErrorSummaryButton count={errors.length} onClick={() => setClosed(false)} />
      )}
      {!!errors.length && !closed && (
        <Alert
          data-testid="upgrade-errors"
          title="Errors upgrading to Grafana Alerting"
          severity="error"
          onRemove={() => setClosed(true)}
        >
          {expanded && errors.map((item, idx) => <div key={idx}>{item}</div>)}
          {!expanded && (
            <>
              <div>{errors[0]}</div>
              {errors.length >= 2 && (
                <Button
                  className={styles.moreButton}
                  fill="text"
                  icon="angle-right"
                  size="sm"
                  onClick={() => setExpanded(true)}
                >
                  {errors.length - 1} more {pluralize('error', errors.length - 1)}
                </Button>
              )}
            </>
          )}
        </Alert>
      )}
    </>
  );
};

export const getStyles = (theme: GrafanaTheme2) => ({
  wrapperMargin: css`
    ${theme.breakpoints.up('md')} {
      margin-left: 36px;
    }
  `,
  emptyMessage: css`
    padding: ${theme.spacing(1)};
  `,
  wrapper: css`
    width: auto;
    border-radius: ${theme.shape.radius.default};
  `,
  pagination: css`
    display: flex;
    margin: 0;
    padding-top: ${theme.spacing(1)};
    padding-bottom: ${theme.spacing(0.25)};
    justify-content: center;
    border-left: 1px solid ${theme.colors.border.medium};
    border-right: 1px solid ${theme.colors.border.medium};
    border-bottom: 1px solid ${theme.colors.border.medium};
  `,
  button: css`
    padding: 0 ${theme.spacing(2)};
  `,
  rulesTable: css`
    margin-top: ${theme.spacing(3)};
  `,
  warningIcon: css`
      fill: ${theme.colors.warning.text};
  `,

  rowWrapper: css``,
  header: css`
      display: flex;
      flex-direction: row;
      align-items: center;
      padding: ${theme.spacing(1)} ${theme.spacing(1)} ${theme.spacing(1)} 0;
      flex-wrap: nowrap;
      border-bottom: 1px solid ${theme.colors.border.weak};

      &:hover {
        background-color: ${theme.components.table.rowHoverBackground};
      }
  `,
  headerStats: css`
    flex-shrink: 0;

    span {
      vertical-align: middle;
    }

    ${theme.breakpoints.down('sm')} {
      order: 2;
      width: 100%;
      padding-left: ${theme.spacing(1)};
    }
  `,
  groupName: css`
      margin-left: ${theme.spacing(1)};
      margin-bottom: 0;
      cursor: pointer;

      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
  `,
  spacer: css`
      flex: 1;
  `,
  collapseToggle: css`
      background: none;
      border: none;
      margin-top: -${theme.spacing(1)};
      margin-bottom: -${theme.spacing(1)};

      svg {
        margin-bottom: 0;
      }
  `,
  actionsSeparator: css`
      margin: 0 ${theme.spacing(2)};
  `,
  actionIcons: css`
      width: 95px;
      align-items: center;

      flex-shrink: 0;
  `,


  sectionHeader: css`
    display: flex;
    justify-content: space-between;
    margin-bottom: ${theme.spacing(1)};
    margin-top: ${theme.spacing(1)};
  `,
  searchWrapper: css`
    margin-bottom: ${theme.spacing(2)};
  `,
  spinner: css`
    text-align: center;
    padding: ${theme.spacing(2)};
  `,
  sectionPagination: getPaginationStyles(theme),
  textLink: css`
    color: ${theme.colors.text.link};
    cursor: pointer;

    &:hover {
      text-decoration: underline;
    }
  `,

  statsContainer: css`
    display: flex;
    flex-direction: row;
    align-items: center;
  `,
  tabContent: css`
    margin-top: ${theme.spacing(2)};
  `,
  moreButton: css`
    padding: 0;
  `,
  tableBadges: css`
    justify-content: flex-end;
  `
});

export default UpgradePage;

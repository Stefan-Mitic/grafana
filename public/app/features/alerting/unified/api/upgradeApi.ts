import {alertingApi} from './alertingApi';

export interface OrgMigrationSummary {
  orgId: number;
  migratedDashboards: DashboardUpgrade[];
  migratedChannels: ContactPair[];
  createdFolders: string[];
  errors: string[];
}

export interface DashboardUpgrade {
  migratedAlerts: AlertPair[];
  dashboardId: number;
  dashboardUid: string;
  dashboardName: string;
  folderUid: string;
  folderName: string;
  newFolderUid?: string;
  newFolderName?: string;
  provisioned: boolean;
  errors: string[];
}

export interface AlertPair {
  legacyAlert: LegacyAlert;
  alertRule?: AlertRuleUpgrade;
  error?: string;
}

export interface ContactPair {
  legacyChannel: LegacyChannel;
  contactPoint?: ContactPointUpgrade;
  provisioned: boolean;
  error?: string;
}

export interface LegacyAlert {
  id: number;
  dashboardId: number;
  panelId: number;
  name: string;
  paused: boolean;
  silenced: boolean;
  executionError: string;
  frequency: number;
  for: number;

  modified: boolean;
}

export interface AlertRuleUpgrade {
  uid: string;
  title: string;
  dashboardUid?: string | null;
  panelId?: number | null;
  noDataState: string;
  execErrState: string;
  for: number;
  annotations: { [key: string]: string };
  labels: { [key: string]: string };
  isPaused: boolean;

  modified: boolean;

  // Computed.
  sendsTo: string[];
}

export interface LegacyChannel {
  id: number;
  uid: string;
  name: string;
  type: string;
  sendReminder: boolean;
  disableResolveMessage: boolean;
  frequency: number;
  isDefault: boolean;
  modified: boolean;
}

export interface ContactPointUpgrade {
  name: string;
  uid: string;
  type: string;
  disableResolveMessage: boolean;
  modified: boolean;
}

export const upgradeApi = alertingApi.injectEndpoints({
  endpoints: (build) => ({
    upgradeOrg: build.mutation<OrgMigrationSummary, void>({
      query: () => ({
        url: `/api/v1/upgrade`,
        method: 'POST',
      }),
      invalidatesTags: ['OrgMigrationSummary'],
    }),
    cancelOrgUpgrade: build.mutation<OrgMigrationSummary, void>({
      query: () => ({
        url: `/api/v1/upgrade`,
        method: 'DELETE',
      }),
      invalidatesTags: ['OrgMigrationSummary'],
    }),
    getOrgUpgradeSummary: build.query<OrgMigrationSummary, void>({
      query: () => ({
        url: `/api/v1/upgrade`,
      }),
      providesTags: ['OrgMigrationSummary'],
      transformResponse: (summary: OrgMigrationSummary): OrgMigrationSummary => {
        summary.migratedDashboards = summary.migratedDashboards ?? [];
        summary.migratedChannels = summary.migratedChannels ?? [];
        summary.errors = summary.errors ?? [];

        const channelMap: Record<string, string> = {};
        const defaultContacts = new Set(summary.migratedChannels.filter((channelPair) => channelPair.legacyChannel?.isDefault && channelPair.contactPoint?.name).map((channelPair) => channelPair.contactPoint?.name ?? ''));
        summary.migratedChannels.forEach((channelPair) => {
          if (channelPair.contactPoint?.name && !channelPair?.legacyChannel?.isDefault && channelPair?.legacyChannel?.uid) {
            channelMap[`__contacts_${channelPair.legacyChannel.uid}__`] = channelPair.contactPoint.name;
          }
        });

        // Sort to show the most problematic rows first.
        summary.migratedDashboards.forEach((dashUpgrade) => {
          dashUpgrade.migratedAlerts = dashUpgrade.migratedAlerts ?? [];
          dashUpgrade.errors = dashUpgrade.errors ?? [];
          dashUpgrade.migratedAlerts.sort((a, b) => {
            const byError = (b.error??'').localeCompare(a.error??'');
            if (byError !== 0) {
              return byError;
            }
            return (a.legacyAlert?.name??'').localeCompare(b.legacyAlert?.name??'');
          });

          // Calculate sends to fields.
          dashUpgrade.migratedAlerts.forEach((alertPair) => {
            if (!alertPair?.alertRule) {
              return
            }
            const defaults = new Set(defaultContacts);
            alertPair.alertRule.sendsTo = [...Object.keys(alertPair.alertRule?.labels??{}).reduce((acc, cur) => {
              if (channelMap[cur]) {
                acc.add(channelMap[cur]);
              }
              return acc;
            }, defaults)];
          });
        });
        summary.migratedDashboards.sort((a, b) => {
          const byErrors = b.errors.length - a.errors.length;
          if (byErrors !== 0) {
            return byErrors;
          }
          const byNestedErrors = b.migratedAlerts.filter((a) => a.error).length - a.migratedAlerts.filter((a) => a.error).length;
          if (byNestedErrors !== 0) {
            return byNestedErrors;
          }
          const byFolder = a.folderName.localeCompare(b.folderName);
          if (byFolder !== 0) {
            return byFolder;
          }
          return a.dashboardName.localeCompare(b.dashboardName);
        });

        // Sort contacts.
        summary.migratedChannels.sort((a, b) => {
          const byErrors = (b.error ? 1 : 0) - (a.error ? 1 : 0);
          if (byErrors !== 0) {
            return byErrors;
          }
          return (a.contactPoint?.name??'').localeCompare(b.contactPoint?.name??'');
        })

        return summary;
      }
    }),
  })
})

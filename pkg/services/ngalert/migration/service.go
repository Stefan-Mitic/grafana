package migration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/grafana/grafana/pkg/infra/db"
	"github.com/grafana/grafana/pkg/infra/log"
	"github.com/grafana/grafana/pkg/infra/serverlock"
	"github.com/grafana/grafana/pkg/services/accesscontrol"
	"github.com/grafana/grafana/pkg/services/dashboards"
	"github.com/grafana/grafana/pkg/services/folder"
	apiModels "github.com/grafana/grafana/pkg/services/ngalert/api/tooling/definitions"
	migrationStore "github.com/grafana/grafana/pkg/services/ngalert/migration/store"
	ngmodels "github.com/grafana/grafana/pkg/services/ngalert/models"
	"github.com/grafana/grafana/pkg/services/secrets"
	"github.com/grafana/grafana/pkg/setting"
)

// actionName is the unique row-level lock name for serverlock.ServerLockService.
const actionName = "alerting migration"

//nolint:stylecheck
var ForceMigrationError = fmt.Errorf("Grafana has already been migrated to Unified Alerting. Any alert rules created while using Unified Alerting will be deleted by rolling back. Set force_migration=true in your grafana.ini and restart Grafana to roll back and delete Unified Alerting configuration data.")

const anyOrg = 0

type MigrationService struct {
	lock           *serverlock.ServerLockService
	mtx            sync.Mutex
	cfg            *setting.Cfg
	log            log.Logger
	store          db.DB
	migrationStore migrationStore.Store

	encryptionService    secrets.Service
	dashboardPermissions accesscontrol.DashboardPermissionsService
}

func ProvideService(
	lock *serverlock.ServerLockService,
	cfg *setting.Cfg,
	store db.DB,
	migrationStore migrationStore.Store,
	encryptionService secrets.Service,
	dashboardPermissions accesscontrol.DashboardPermissionsService,
) (*MigrationService, error) {
	return &MigrationService{
		lock:                 lock,
		log:                  log.New("ngalert.migration"),
		cfg:                  cfg,
		store:                store,
		migrationStore:       migrationStore,
		encryptionService:    encryptionService,
		dashboardPermissions: dashboardPermissions,
	}, nil
}

// MigrateChannel migrates a single legacy notification channel to a unified alerting contact point.
func (ms *MigrationService) MigrateChannel(ctx context.Context, orgID int64, channelID int64) (*apiModels.ContactPair, error) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	var pair *apiModels.ContactPair
	err := ms.store.InTransaction(ctx, func(ctx context.Context) error {
		om := ms.newOrgMigration(orgID)
		summary, err := om.migrationStore.GetOrgMigrationSummary(ctx, orgID)
		if err != nil {
			return fmt.Errorf("failed to get org migration summary for org %d: %w", orgID, err)
		}
		om.summary = summary

		amConfig, err := om.migrationStore.GetAlertmanagerConfig(ctx, orgID)
		if err != nil {
			return fmt.Errorf("failed to get alertmanager config: %w", err)
		}

		channel, err := om.migrationStore.GetNotificationChannel(ctx, orgID, channelID)
		if err != nil {
			return fmt.Errorf("failed to get notification channel: %w", err)
		}

		// Remove ContactPair from summary.
		var keptUpgrades []*apiModels.ContactPair
		for _, up := range om.summary.MigratedChannels {
			if up.LegacyChannel != nil && up.LegacyChannel.ID != channelID {
				keptUpgrades = append(keptUpgrades, up)
			}
		}
		om.summary.MigratedChannels = keptUpgrades

		receiver, route, err := om.migrateChannel(channel)
		if err != nil {
			om.log.Warn(err.Error(), "type", channel.Type, "name", channel.Name, "uid", channel.UID)
			// Fail early for the single channel endpoint.
			return err
		}

		var nestedLegacyChannelRoute *apiModels.Route
		if amConfig == nil {
			// No existing amConfig created from a previous migration.
			amConfig, nestedLegacyChannelRoute = createBaseConfig()
		} else if amConfig.AlertmanagerConfig.Route == nil {
			// No existing base route created from a previous migration.
			amConfig.AlertmanagerConfig.Route, nestedLegacyChannelRoute = createDefaultRoute()
		} else {
			nestedLegacyChannelRoute = extractNestedLegacyRoute(amConfig)
			if nestedLegacyChannelRoute == nil {
				// No existing nested route created from a previous migration, create a new one.
				nestedLegacyChannelRoute = createNestedLegacyRoute()
				// Add it as the first of the top-level routes.
				amConfig.AlertmanagerConfig.Route.Routes = append([]*apiModels.Route{nestedLegacyChannelRoute}, amConfig.AlertmanagerConfig.Route.Routes...)
			}
		}

		// Remove existing receiver with the same uid as the channel and all nested routes that reference it.
		for i, recv := range amConfig.AlertmanagerConfig.Receivers {
			for _, integration := range recv.GrafanaManagedReceivers {
				if integration.UID == channel.UID {
					amConfig.AlertmanagerConfig.Receivers = append(amConfig.AlertmanagerConfig.Receivers[:i], amConfig.AlertmanagerConfig.Receivers[i+1:]...)

					// Remove all routes that reference this receiver in the nested route.
					var keptRoutes []*apiModels.Route
					for j, rte := range nestedLegacyChannelRoute.Routes {
						if rte.Receiver != recv.Name {
							keptRoutes = append(keptRoutes, nestedLegacyChannelRoute.Routes[j])
						}
					}
					nestedLegacyChannelRoute.Routes = keptRoutes
				}
			}
		}

		nestedLegacyChannelRoute.Routes = append(nestedLegacyChannelRoute.Routes, route)
		amConfig.AlertmanagerConfig.Receivers = append(amConfig.AlertmanagerConfig.Receivers, receiver)
		pair = newContactPair(channel, receiver, route, nil)
		om.summary.MigratedChannels = append(om.summary.MigratedChannels, pair)

		if err := om.validateAlertmanagerConfig(amConfig); err != nil {
			return fmt.Errorf("failed to validate AlertmanagerConfig: %w", err)
		}

		ms.log.Info("Writing alertmanager config", "orgID", orgID, "receivers", len(amConfig.AlertmanagerConfig.Receivers), "routes", len(amConfig.AlertmanagerConfig.Route.Routes))
		if err := ms.migrationStore.SaveAlertmanagerConfiguration(ctx, orgID, amConfig); err != nil {
			return fmt.Errorf("failed to write AlertmanagerConfig: %w", err)
		}

		err = ms.migrationStore.SetOrgMigrationSummary(ctx, orgID, om.summary)
		if err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return pair, nil
}

// MigrateAllChannels migrates all legacy notification channel to unified alerting contact points.
func (ms *MigrationService) MigrateAllChannels(ctx context.Context, orgID int64) ([]*apiModels.ContactPair, error) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	var pairs []*apiModels.ContactPair
	err := ms.store.InTransaction(ctx, func(ctx context.Context) error {
		om := ms.newOrgMigration(orgID)
		summary, err := om.migrationStore.GetOrgMigrationSummary(ctx, orgID)
		if err != nil {
			return fmt.Errorf("failed to get org migration summary for org %d: %w", orgID, err)
		}
		om.summary = summary

		amConfig, err := om.migrationStore.GetAlertmanagerConfig(ctx, orgID)
		if err != nil {
			return fmt.Errorf("failed to get alertmanager config: %w", err)
		}

		channels, err := om.migrationStore.GetNotificationChannels(ctx, orgID)
		if err != nil {
			return fmt.Errorf("failed to get notification channel: %w", err)
		}

		// Remove ContactPairs from summary.
		om.summary.MigratedChannels = nil

		// Cleanup existing migrated routes.
		var nestedLegacyChannelRoute *apiModels.Route
		if amConfig == nil {
			// No existing amConfig created from a previous migration.
			amConfig, nestedLegacyChannelRoute = createBaseConfig()
		} else if amConfig.AlertmanagerConfig.Route == nil {
			// No existing base route created from a previous migration.
			amConfig.AlertmanagerConfig.Route, nestedLegacyChannelRoute = createDefaultRoute()
		} else {
			// Remove existing nested routes created from a previous migration and add new nested route as the first of the top-level routes.
			nestedLegacyChannelRoute = createNestedLegacyRoute()
			keptRoutes := []*apiModels.Route{nestedLegacyChannelRoute}
			for _, r := range amConfig.AlertmanagerConfig.Route.Routes {
				if len(r.ObjectMatchers) != 1 || r.ObjectMatchers[0].Name != UseLegacyChannelsLabel {
					keptRoutes = append(keptRoutes, r)
				}
			}
			amConfig.AlertmanagerConfig.Route.Routes = keptRoutes
		}

		// Remove all existing receivers with the same uid as a channel.
		uids := map[string]struct{}{}
		for _, channel := range channels {
			uids[channel.UID] = struct{}{}
		}
		var keptReceivers []*apiModels.PostableApiReceiver
		for _, recv := range amConfig.AlertmanagerConfig.Receivers {
			matched := false
			for _, integration := range recv.GrafanaManagedReceivers {
				if _, ok := uids[integration.UID]; ok {
					matched = true
					break
				}
			}
			if !matched {
				keptReceivers = append(keptReceivers, recv)
			}
		}
		amConfig.AlertmanagerConfig.Receivers = keptReceivers

		for _, channel := range channels {
			receiver, route, err := om.migrateChannel(channel)
			if err != nil {
				om.log.Warn(err.Error(), "type", channel.Type, "name", channel.Name, "uid", channel.UID)
				pair := newContactPair(channel, receiver, route, err)
				pairs = append(pairs, pair)
				om.summary.MigratedChannels = append(om.summary.MigratedChannels, pair)
			}

			nestedLegacyChannelRoute.Routes = append(nestedLegacyChannelRoute.Routes, route)
			amConfig.AlertmanagerConfig.Receivers = append(amConfig.AlertmanagerConfig.Receivers, receiver)
			pair := newContactPair(channel, receiver, route, nil)
			pairs = append(pairs, pair)
			om.summary.MigratedChannels = append(om.summary.MigratedChannels, pair)
		}

		if err := om.validateAlertmanagerConfig(amConfig); err != nil {
			return fmt.Errorf("failed to validate AlertmanagerConfig: %w", err)
		}

		ms.log.Info("Writing alertmanager config", "orgID", orgID, "receivers", len(amConfig.AlertmanagerConfig.Receivers), "routes", len(amConfig.AlertmanagerConfig.Route.Routes))
		if err := ms.migrationStore.SaveAlertmanagerConfiguration(ctx, orgID, amConfig); err != nil {
			return fmt.Errorf("failed to write AlertmanagerConfig: %w", err)
		}

		err = ms.migrationStore.SetOrgMigrationSummary(ctx, orgID, om.summary)
		if err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return pairs, nil
}

func extractNestedLegacyRoute(config *apiModels.PostableUserConfig) *apiModels.Route {
	for _, r := range config.AlertmanagerConfig.Route.Routes {
		if len(r.ObjectMatchers) == 1 && r.ObjectMatchers[0].Name == UseLegacyChannelsLabel {
			return r
		}
	}
	return nil
}

// MigrateAlert migrates a single dashboard alert from legacy alerting to unified alerting.
func (ms *MigrationService) MigrateAlert(ctx context.Context, orgID int64, dashboardID int64, panelID int64) (*apiModels.DashboardUpgrade, error) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	var du *apiModels.DashboardUpgrade
	err := ms.store.InTransaction(ctx, func(ctx context.Context) error {
		om := ms.newOrgMigration(orgID)
		summary, err := om.migrationStore.GetOrgMigrationSummary(ctx, orgID)
		if err != nil {
			return fmt.Errorf("failed to get org migration summary for org %d: %w", orgID, err)
		}
		om.summary = summary

		alert, err := ms.migrationStore.GetDashboardAlert(ctx, orgID, dashboardID, panelID)
		if err != nil {
			return fmt.Errorf("failed to get alert: %w", err)
		}

		// Find existing alert upgrade.
		du = om.summary.GetDashboardUpgrade(alert.DashboardID)
		if du == nil {
			du = newDashboardUpgrade(alert.DashboardID)
		}
		l := om.log.New(
			"dashboardTitle", du.DashboardName,
			"dashboardUID", du.DashboardUID,
			"newFolderName", du.NewFolderName,
			"newFolderUID", du.NewFolderUID,
			"folderUID", du.FolderUID,
			"folderName", du.FolderName,
			"ruleID", alert.ID,
			"ruleName", alert.Name,
		)

		// Cleanup.
		if existingPair := du.PopAlertPairByPanelID(panelID); existingPair != nil {
			// Alert already migrated, we delete existing and migrate again.
			if existingPair.AlertRule != nil && existingPair.AlertRule.UID != "" {
				err := om.migrationStore.DeleteAlertRules(ctx, orgID, existingPair.AlertRule.UID)
				if err != nil {
					return fmt.Errorf("failed to delete existing alert rule: %w", err)
				}
			}
		}

		dash, err := ms.migrationStore.GetDashboard(ctx, orgID, dashboardID)
		if err != nil {
			return fmt.Errorf("failed to get dashboard [ID: %d]: %w", dashboardID, err)
		}

		provisioned, err := ms.migrationStore.IsProvisioned(ctx, orgID, dash.UID)
		if err != nil {
			return fmt.Errorf("failed to get provisioned status: %w", err)
		}
		// If the provisioned status has changed, they need to use MigrateDashboard
		if provisioned != du.Provisioned {
			return fmt.Errorf("provisioned status has changed for dashboard %s, must re-upgrade entire dashboard", dash.UID)
		}

		// For the purposes of single alert migration, we assume nothing has changed W.R.T the migrated folder.
		// Those changes will be handled, by MigrateDashboard.
		f, err := om.folderHelper.migrationStore.GetFolder(ctx, &folder.GetFolderQuery{UID: &du.NewFolderUID, OrgID: orgID, SignedInUser: getMigrationUser(orgID)})
		if err != nil {
			if errors.Is(err, dashboards.ErrFolderNotFound) {
				return fmt.Errorf("folder with uid %v not found", du.NewFolderUID)
			}
			return fmt.Errorf("failed to get folder with uid %s: %w", du.NewFolderUID, err)
		}

		alertRule, err := om.migrateAlert(ctx, l, alert, dash, f)
		if err != nil {
			du.AddAlertErrors(err, alert)
			return nil
		}

		// We don't deduplicate alert rule titles in the single alert upgrade. Leave this to MigrateDashboard.

		pair := du.AddAlert(alert)
		attachAlertRule(pair, alertRule)

		l.Info("Inserting migrated alert rules", "count", 1, "provisioned", provisioned)
		err = ms.migrationStore.InsertAlertRules(ctx, orgID, []ngmodels.AlertRule{*alertRule}, provisioned)
		if err != nil {
			return fmt.Errorf("failed to insert alert rules: %w", err)
		}

		// We don't create new folders here, so no need to upgrade summary.CreatedFolders.
		err = ms.migrationStore.SetOrgMigrationSummary(ctx, orgID, om.summary)
		if err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return du, nil
}

// MigrateDashboardAlerts migrates all legacy dashboard alerts from a single dashboard to unified alerting.
func (ms *MigrationService) MigrateDashboardAlerts(ctx context.Context, orgID int64, dashboardID int64) (*apiModels.DashboardUpgrade, error) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	var du *apiModels.DashboardUpgrade
	err := ms.store.InTransaction(ctx, func(ctx context.Context) error {
		om := ms.newOrgMigration(orgID)
		summary, err := om.migrationStore.GetOrgMigrationSummary(ctx, orgID)
		if err != nil {
			return fmt.Errorf("failed to get org migration summary for org %d: %w", orgID, err)
		}
		om.summary = summary

		alerts, err := ms.migrationStore.GetDashboardAlerts(ctx, orgID, dashboardID)
		if err != nil {
			return fmt.Errorf("failed to get alerts: %w", err)
		}

		// Cleanup.
		if existingDu := om.summary.PopDashboardUpgrade(dashboardID); existingDu != nil {
			// Dashboard already migrated, we delete existing and migrate again.
			ruleUids := make([]string, 0, len(existingDu.MigratedAlerts))
			for _, pair := range existingDu.MigratedAlerts {
				if pair.AlertRule != nil && pair.AlertRule.UID != "" {
					ruleUids = append(ruleUids, pair.AlertRule.UID)
				}
			}
			err := om.migrationStore.DeleteAlertRules(ctx, orgID, ruleUids...)
			if err != nil {
				return fmt.Errorf("failed to delete existing alert rule: %w", err)
			}

			// Delete newly created folder if one exists.
			isNewAlertingFolder := existingDu.NewFolderUID != "" && existingDu.NewFolderUID != existingDu.FolderUID
			if isNewAlertingFolder {
				// Remove uid from summary.createdFolders
				found := false
				for i, uid := range om.summary.CreatedFolders {
					if uid == existingDu.NewFolderUID {
						om.summary.CreatedFolders = append(om.summary.CreatedFolders[:i], om.summary.CreatedFolders[i+1:]...)
						found = true
						break
					}
				}
				// Safety check to prevent deleting folders that were not created by this migration.
				if found {
					err := om.folderHelper.migrationStore.DeleteFolders(ctx, orgID, existingDu.NewFolderUID)
					if err != nil {
						return fmt.Errorf("failed to delete folder '%s': %w", existingDu.NewFolderName, err)
					}
				}
			}
		}

		newDu, err := om.migrateDashboard(ctx, dashboardID, alerts)
		if err != nil {
			l := om.log.New(
				"dashboardTitle", newDu.DashboardName,
				"dashboardUID", newDu.DashboardUID,
				"newFolderName", newDu.NewFolderName,
				"newFolderUID", newDu.NewFolderUID,
				"folderUID", newDu.FolderUID,
				"folderName", newDu.FolderName,
			)
			l.Warn("failed to migrate dashboard", "alertCount", len(alerts), "error", err)
			if newDu == nil {
				newDu = newDashboardUpgrade(dashboardID)
			}
			newDu.AddAlertErrors(err, alerts...)
		}
		om.summary.MigratedDashboards = append(om.summary.MigratedDashboards, newDu)

		for _, f := range om.folderHelper.createdFolders {
			om.summary.CreatedFolders = append(om.summary.CreatedFolders, f.UID)
		}
		err = ms.migrationStore.SetOrgMigrationSummary(ctx, orgID, om.summary)
		if err != nil {
			return err
		}

		du = newDu
		return nil
	})
	if err != nil {
		return nil, err
	}

	return du, nil
}

// MigrateOrg executes the migration for a single org.
func (ms *MigrationService) MigrateOrg(ctx context.Context, orgID int64) (*apiModels.OrgMigrationSummary, error) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	var summary *apiModels.OrgMigrationSummary
	err := ms.store.InTransaction(ctx, func(ctx context.Context) error {
		ms.log.Info("Starting legacy migration for org", "orgID", orgID)
		om, err := ms.migrateOrg(ctx, orgID)
		if err != nil {
			return fmt.Errorf("failed to migrate org %d: %w", orgID, err)
		}
		summary = om.summary
		return nil
	})
	if err != nil {
		return nil, err
	}

	return summary, nil
}

// MigrateAllOrgs executes the migration for all orgs.
func (ms *MigrationService) MigrateAllOrgs(ctx context.Context) error {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	return ms.store.InTransaction(ctx, func(ctx context.Context) error {
		ms.log.Info("Migrating all orgs")
		return ms.migrateAllOrgs(ctx)
	})
}

func (ms *MigrationService) GetOrgMigrationSummary(ctx context.Context, orgID int64) (*apiModels.OrgMigrationSummary, error) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	var summary *apiModels.OrgMigrationSummary
	err := ms.store.InTransaction(ctx, func(ctx context.Context) error {
		var err error
		summary, err = ms.migrationStore.GetOrgMigrationSummary(ctx, orgID)
		if err != nil {
			return fmt.Errorf("failed to get org migration summary for org %d: %w", orgID, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return summary, nil
}

// Run starts the migration. This will either migrate from legacy alerting to unified alerting or revert the migration.
// If the migration status in the kvstore is not set and unified alerting is enabled, the migration will be executed.
// If the migration status in the kvstore is set and both unified alerting is disabled and ForceMigration is set to true, the migration will be reverted.
func (ms *MigrationService) Run(ctx context.Context) error {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	var errMigration error
	errLock := ms.lock.LockExecuteAndRelease(ctx, actionName, time.Minute*10, func(context.Context) {
		ms.log.Info("Starting")
		errMigration = ms.store.InTransaction(ctx, func(ctx context.Context) error {
			migrated, err := ms.migrationStore.IsMigrated(ctx, anyOrg)
			if err != nil {
				return fmt.Errorf("getting migration status: %w", err)
			}
			if migrated == ms.cfg.UnifiedAlerting.IsEnabled() {
				// Nothing to do.
				ms.log.Info("No migrations to run")
				return nil
			}

			if migrated {
				// If legacy alerting is also disabled, there is nothing to do
				if setting.AlertingEnabled != nil && !*setting.AlertingEnabled {
					return nil
				}

				// Safeguard to prevent data loss when reverting from UA to LA.
				if !ms.cfg.ForceMigration {
					return ForceMigrationError
				}

				// Revert migration
				ms.log.Info("Reverting legacy migration")
				err := ms.migrationStore.RevertAllOrgs(ctx)
				if err != nil {
					return fmt.Errorf("reverting migration: %w", err)
				}
				ms.log.Info("Legacy migration reverted")
				return nil
			}

			ms.log.Info("Starting legacy migration")
			err = ms.migrateAllOrgs(ctx)
			if err != nil {
				return fmt.Errorf("executing migration: %w", err)
			}

			ms.log.Info("Completed legacy migration")
			return nil
		})
	})
	if errLock != nil {
		ms.log.Warn("Server lock for alerting migration already exists")
		return nil
	}
	if errMigration != nil {
		return fmt.Errorf("migration failed: %w", errMigration)
	}
	return nil
}

// IsDisabled returns true if the cfg is nil.
func (ms *MigrationService) IsDisabled() bool {
	return ms.cfg == nil
}

// RevertOrg reverts the migration, deleting all unified alerting resources such as alert rules, alertmanager
// configurations, and silence files for a single organization.
// In addition, it will delete all folders and permissions originally created by this migration.
func (ms *MigrationService) RevertOrg(ctx context.Context, orgID int64) error {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	ms.log.Info("Reverting legacy migration for org", "orgID", orgID)
	return ms.store.InTransaction(ctx, func(ctx context.Context) error {
		return ms.migrationStore.RevertOrg(ctx, orgID)
	})
}

// RevertAllOrgs reverts the migration for all orgs, deleting all unified alerting resources such as alert rules, alertmanager configurations, and silence files.
// In addition, it will delete all folders and permissions originally created by this migration.
func (ms *MigrationService) RevertAllOrgs(ctx context.Context) error {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	ms.log.Info("Reverting legacy migration for all orgs")
	return ms.store.InTransaction(ctx, func(ctx context.Context) error {
		return ms.migrationStore.RevertAllOrgs(ctx)
	})
}

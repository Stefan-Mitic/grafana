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

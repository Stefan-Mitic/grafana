package migration

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/grafana/grafana/pkg/infra/db"
	"github.com/grafana/grafana/pkg/infra/log"
	"github.com/grafana/grafana/pkg/infra/serverlock"
	"github.com/grafana/grafana/pkg/services/accesscontrol"
	apiModels "github.com/grafana/grafana/pkg/services/ngalert/api/tooling/definitions"
	migrationStore "github.com/grafana/grafana/pkg/services/ngalert/migration/store"
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

//// MigrateAlert migrates a single dashboard alert from legacy alerting to unified alerting.
//func (ms *MigrationService) MigrateAlert(ctx context.Context, alert *legacymodels.Alert) (*ngmodels.AlertRule, error) {
//	// TODO: WAY WAY TOO MESSY, needs to be summary logic needs cleaning up now before it gets more out of hand.
//	ms.mtx.Lock()
//	defer ms.mtx.Unlock()
//
//	om := ms.newOrgMigration(alert.OrgID)
//	summary, err := om.migrationStore.GetOrgMigrationSummary(ctx, alert.OrgID)
//	if err != nil {
//		return nil, fmt.Errorf("failed to get org migration summary for org %d: %w", alert.OrgID, err)
//	}
//	om.summary = summary
//
//	// TODO: should run the same code here as org migration so that the summary is updated appropriately
//	dash, err := ms.migrationStore.GetDashboard(ctx, alert.OrgID, alert.DashboardID)
//	if err != nil {
//		om.summary.MigratedAlerts = append(om.summary.MigratedAlerts, newAlertPair(alert, err))
//		return nil, fmt.Errorf("failed to get dashboard [ID: %d]: %w", alert.DashboardID, err)
//	}
//	l := ms.log.New("ruleID", alert.ID, "ruleName", alert.Name, "dashboardTitle", dash.Title, "dashboardUID", dash.UID)
//
//	provisioned, err := ms.migrationStore.IsProvisioned(ctx, alert.OrgID, dash.UID)
//	if err != nil {
//		l.Warn("failed to get provisioned status for dashboard", "error", err)
//		om.summary.Errors = append(om.summary.Errors, err.Error()) //TODO not sure we should be adding to the summary errors here.
//	}
//
//	dashFolder, migratedFolder, err := om.folderHelper.getOrCreateMigratedFolder(ctx, l, dash)
//	if err != nil {
//		pair := newAlertPair(alert, err)
//		pair.LegacyAlert.DashboardUID = dash.UID
//		pair.LegacyAlert.DashboardName = dash.Title
//		pair.Provisioned = provisioned
//		if dashFolder != nil {
//			pair.LegacyAlert.FolderUID = dashFolder.UID
//			pair.LegacyAlert.FolderName = dashFolder.Title
//		}
//		om.summary.MigratedAlerts = append(om.summary.MigratedAlerts, pair)
//		return nil, fmt.Errorf("failed to get or create folder for dashboard %s [ID: %d]: %w", dash.Title, dash.ID, err)
//	}
//	l = l.New("folderUID", migratedFolder.UID, "folderName", dashFolder.Title, "migratedFolderUID", migratedFolder.UID, "migratedFolderName", migratedFolder.Title)
//
//	pair := newAlertPair(alert, nil)
//	pair.LegacyAlert.DashboardUID = dash.UID
//	pair.LegacyAlert.DashboardName = dash.Title
//	pair.Provisioned = provisioned
//	if dashFolder != nil {
//		pair.LegacyAlert.FolderUID = dashFolder.UID
//		pair.LegacyAlert.FolderName = dashFolder.Title
//	}
//	alertRule, err := om.migrateAlert(ctx, l, alert, dash, migratedFolder)
//	if err != nil {
//		pair.Error = err.Error()
//		om.summary.MigratedAlerts = append(om.summary.MigratedAlerts, pair)
//		return nil, err
//	}
//
//	//TODO deduplicate alert name, maybe do it this new way in the normal routine as well in case of existing alerts?
//
//	om.silences = append(om.silences, createSilences(l, alert, alertRule)...)
//
//	addAlertRule(pair, alertRule, alertRule.NamespaceUID, migratedFolder.Title)
//	om.summary.MigratedAlerts = append(om.summary.MigratedAlerts, pair)
//
//	l.Info("Inserting migrated alert rules", "count", 1, "provisioned", provisioned)
//	err = ms.migrationStore.InsertAlertRules(ctx, alert.OrgID, []ngmodels.AlertRule{*alertRule}, provisioned)
//	if err != nil {
//		om.summary.Errors = append(om.summary.Errors, fmt.Errorf("failed to insert alert rules: %w", err).Error())
//	}
//
//	om.summary.CreatedFolders = make([]string, 0, len(om.folderHelper.createdFolders))
//	for _, f := range om.folderHelper.createdFolders {
//		// TODO: deduplicate.
//		om.summary.CreatedFolders = append(om.summary.CreatedFolders, f.UID)
//	}
//
//	err = ms.migrationStore.SetOrgMigrationSummary(ctx, alert.OrgID, om.summary)
//	if err != nil {
//		return nil, err
//	}
//
//	return alertRule, nil
//}

// MigrateOrg executes the migration for a single org.
func (ms *MigrationService) MigrateOrg(ctx context.Context, orgID int64) (*apiModels.OrgMigrationSummary, error) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	var summary *apiModels.OrgMigrationSummary
	err := ms.store.InTransaction(ctx, func(ctx context.Context) error {
		ms.log.Info("Starting legacy migration for org %d", orgID)
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
	ms.log.Info("Reverting legacy migration for org %d", orgID)
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

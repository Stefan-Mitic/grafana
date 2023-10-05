package migration

import (
	"context"
	"fmt"
	"sync"
	"time"

	pb "github.com/prometheus/alertmanager/silence/silencepb"

	"github.com/grafana/grafana/pkg/infra/db"
	"github.com/grafana/grafana/pkg/infra/log"
	"github.com/grafana/grafana/pkg/infra/serverlock"
	"github.com/grafana/grafana/pkg/services/accesscontrol"
	legacymodels "github.com/grafana/grafana/pkg/services/alerting/models"
	"github.com/grafana/grafana/pkg/services/folder"
	migmodels "github.com/grafana/grafana/pkg/services/ngalert/migration/models"
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

// MigrateChannel migrates a single legacy notification channel to a unified alerting contact point.
func (ms *MigrationService) MigrateChannel(ctx context.Context, orgID int64, channelID int64, skipExisting bool) (migmodels.OrgMigrationSummary, error) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	var summary migmodels.OrgMigrationSummary
	err := ms.store.InTransaction(ctx, func(ctx context.Context) error {
		om := ms.newOrgMigration(orgID)
		state, err := om.migrationStore.GetOrgMigrationState(ctx, orgID)
		if err != nil {
			return fmt.Errorf("failed to get org migration summary for org %d: %w", orgID, err)
		}
		om.state = state

		channel, err := om.migrationStore.GetNotificationChannel(ctx, orgID, channelID)
		if err != nil {
			return fmt.Errorf("failed to get notification channel: %w", err)
		}
		pairs, err := om.migrateAndSaveChannels(ctx, []*legacymodels.AlertNotification{channel}, skipExisting)
		if err != nil {
			return err
		}

		err = ms.migrationStore.SetOrgMigrationState(ctx, orgID, om.state)
		if err != nil {
			return err
		}
		summary.CountChannels(pairs...)
		return nil
	})
	if err != nil {
		return summary, err
	}

	return summary, nil
}

// MigrateAllChannels migrates all legacy notification channel to unified alerting contact points.
func (ms *MigrationService) MigrateAllChannels(ctx context.Context, orgID int64, skipExisting bool) (migmodels.OrgMigrationSummary, error) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	var summary migmodels.OrgMigrationSummary
	err := ms.store.InTransaction(ctx, func(ctx context.Context) error {
		om := ms.newOrgMigration(orgID)
		state, err := om.migrationStore.GetOrgMigrationState(ctx, orgID)
		if err != nil {
			return fmt.Errorf("failed to get org migration summary for org %d: %w", orgID, err)
		}
		om.state = state

		channels, err := om.migrationStore.GetNotificationChannels(ctx, om.orgID)
		if err != nil {
			return fmt.Errorf("failed to load notification channels: %w", err)
		}
		pairs, err := om.migrateAndSaveChannels(ctx, channels, skipExisting)
		if err != nil {
			return err
		}

		err = ms.migrationStore.SetOrgMigrationState(ctx, orgID, om.state)
		if err != nil {
			return err
		}
		summary.CountChannels(pairs...)

		return nil
	})
	if err != nil {
		return summary, err
	}

	return summary, nil
}

// MigrateAlert migrates a single dashboard alert from legacy alerting to unified alerting.
func (ms *MigrationService) MigrateAlert(ctx context.Context, orgID int64, dashboardID int64, panelID int64, skipExisting bool) (migmodels.OrgMigrationSummary, error) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	var summary migmodels.OrgMigrationSummary
	err := ms.store.InTransaction(ctx, func(ctx context.Context) error {
		om := ms.newOrgMigration(orgID)
		state, err := om.migrationStore.GetOrgMigrationState(ctx, orgID)
		if err != nil {
			return fmt.Errorf("failed to get org migration summary for org %d: %w", orgID, err)
		}
		om.state = state

		// Cleanup.
		var du *migmodels.DashboardUpgrade
		if skipExisting {
			du = om.state.PopDashboardUpgrade(dashboardID)
			if du != nil {
				existing := du.PopAlertPairs(panelID)
				if len(existing) > 0 {
					return fmt.Errorf("alert already migrated")
				}
			}
		} else {
			du = om.state.PopDashboardUpgrade(dashboardID)
			if du != nil {
				for _, existingPair := range du.PopAlertPairs(panelID) {
					if existingPair.AlertRule != nil && existingPair.AlertRule.UID != "" {
						err := om.migrationStore.DeleteAlertRules(ctx, orgID, existingPair.AlertRule.UID)
						if err != nil {
							return fmt.Errorf("delete existing alert rule: %w", err)
						}
					}
				}
			}
		}
		if du == nil {
			return fmt.Errorf("dashboard not migrated")
		}

		alert, err := ms.migrationStore.GetDashboardAlert(ctx, orgID, dashboardID, panelID)
		if err != nil {
			return fmt.Errorf("get alert: %w", err)
		}

		pairs, err := om.migrateAndSaveAlerts(ctx, []*legacymodels.Alert{alert}, *du.DashboardUpgradeInfo)
		if err != nil {
			om.log.Warn("Failed to migrate dashboard alert", "error", err)
			pairs = du.AddAlertErrors(err, alert)
		}
		du.MigratedAlerts = append(du.MigratedAlerts, pairs...)
		om.state.MigratedDashboards = append(om.state.MigratedDashboards, du)

		for _, f := range om.folderHelper.createdFolders {
			om.state.CreatedFolders = append(om.state.CreatedFolders, f.UID)
		}

		// We don't create new folders here, so no need to upgrade summary.CreatedFolders.
		err = ms.migrationStore.SetOrgMigrationState(ctx, orgID, om.state)
		if err != nil {
			return err
		}

		summary.CountDashboardAlerts(pairs...)
		return nil
	})
	if err != nil {
		return summary, err
	}

	return summary, nil
}

// MigrateDashboardAlerts migrates all legacy dashboard alerts from a single dashboard to unified alerting.
func (ms *MigrationService) MigrateDashboardAlerts(ctx context.Context, orgID int64, dashboardID int64, skipExisting bool) (migmodels.OrgMigrationSummary, error) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	var summary migmodels.OrgMigrationSummary
	err := ms.store.InTransaction(ctx, func(ctx context.Context) error {
		om := ms.newOrgMigration(orgID)
		state, err := om.migrationStore.GetOrgMigrationState(ctx, orgID)
		if err != nil {
			return fmt.Errorf("failed to get org migration summary for org %d: %w", orgID, err)
		}
		om.state = state

		alerts, err := ms.migrationStore.GetDashboardAlerts(ctx, orgID, dashboardID)
		if err != nil {
			return fmt.Errorf("failed to get alerts: %w", err)
		}

		du, sum, err := om.migrateAndSaveDashboard(ctx, dashboardID, alerts, skipExisting)
		if err != nil {
			return err
		}

		om.state.MigratedDashboards = append(om.state.MigratedDashboards, du)

		for _, f := range om.folderHelper.createdFolders {
			om.state.CreatedFolders = append(om.state.CreatedFolders, f.UID)
		}
		err = ms.migrationStore.SetOrgMigrationState(ctx, orgID, om.state)
		if err != nil {
			return err
		}
		summary.Add(sum)
		return nil
	})
	if err != nil {
		return summary, err
	}

	return summary, nil
}

// MigrateAllDashboardAlerts migrates all legacy alerts to unified alerting contact points.
func (ms *MigrationService) MigrateAllDashboardAlerts(ctx context.Context, orgID int64, skipExisting bool) (migmodels.OrgMigrationSummary, error) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	var summary migmodels.OrgMigrationSummary
	err := ms.store.InTransaction(ctx, func(ctx context.Context) error {
		om := ms.newOrgMigration(orgID)
		state, err := om.migrationStore.GetOrgMigrationState(ctx, orgID)
		if err != nil {
			return fmt.Errorf("failed to get org migration summary for org %d: %w", orgID, err)
		}
		om.state = state

		summary, err = om.migrateOrgAlerts(ctx, skipExisting)
		if err != nil {
			om.state.AddError(err.Error())
		}

		for _, f := range om.folderHelper.createdFolders {
			om.state.CreatedFolders = append(om.state.CreatedFolders, f.UID)
		}

		err = ms.migrationStore.SetOrgMigrationState(ctx, orgID, om.state)
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return migmodels.OrgMigrationSummary{}, err
	}

	return summary, nil
}

// MigrateOrg executes the migration for a single org.
func (ms *MigrationService) MigrateOrg(ctx context.Context, orgID int64, skipExisting bool) (migmodels.OrgMigrationSummary, error) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	var summary migmodels.OrgMigrationSummary
	err := ms.store.InTransaction(ctx, func(ctx context.Context) error {
		ms.log.Info("Starting legacy migration for org", "orgID", orgID, "skipExisting", skipExisting)
		om := ms.newOrgMigration(orgID)
		state, err := om.migrationStore.GetOrgMigrationState(ctx, orgID)
		if err != nil {
			return fmt.Errorf("failed to get org migration summary for org %d: %w", orgID, err)
		}
		om.state = state

		summary, err = om.migrateOrg(ctx, skipExisting)
		if err != nil {
			return fmt.Errorf("failed to migrate org %d: %w", orgID, err)
		}

		for _, f := range om.folderHelper.createdFolders {
			om.state.CreatedFolders = append(om.state.CreatedFolders, f.UID)
		}

		err = om.migrationStore.SetOrgMigrationState(ctx, orgID, om.state)
		if err != nil {
			return err
		}
		err = om.migrationStore.SetMigrated(ctx, orgID, true)
		if err != nil {
			return fmt.Errorf("setting migration status: %w", err)
		}

		return nil
	})
	if err != nil {
		return summary, err
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

func (ms *MigrationService) GetOrgMigrationState(ctx context.Context, orgID int64) (*migmodels.OrgMigrationState, error) {
	ms.mtx.Lock()
	defer ms.mtx.Unlock()
	var summary *migmodels.OrgMigrationState
	err := ms.store.InTransaction(ctx, func(ctx context.Context) error {
		var err error
		summary, err = ms.migrationStore.GetOrgMigrationState(ctx, orgID)
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

// migrateAllOrgs executes the migration for all orgs.
func (ms *MigrationService) migrateAllOrgs(ctx context.Context) error {
	orgs, err := ms.migrationStore.GetAllOrgs(ctx)
	if err != nil {
		return fmt.Errorf("can't get org list: %w", err)
	}

	for _, o := range orgs {
		migrated, err := ms.migrationStore.IsMigrated(ctx, o.ID)
		if err != nil {
			return fmt.Errorf("getting migration status for org %d: %w", o.ID, err)
		}
		if migrated {
			ms.log.Warn("skipping org, active migration already exists", "orgID", o.ID)
			continue
		}
		om := ms.newOrgMigration(o.ID)
		_, err = om.migrateOrg(ctx, true)
		if err != nil {
			return fmt.Errorf("failed to migrate org %d: %w", o.ID, err)
		}
		if err := om.writeSilencesFile(); err != nil {
			return fmt.Errorf("failed to write silence file: %w", err)
		}

		for _, f := range om.folderHelper.createdFolders {
			om.state.CreatedFolders = append(om.state.CreatedFolders, f.UID)
		}

		err = om.migrationStore.SetOrgMigrationState(ctx, o.ID, om.state)
		if err != nil {
			return err
		}
		err = om.migrationStore.SetMigrated(ctx, o.ID, true)
		if err != nil {
			return fmt.Errorf("setting migration status: %w", err)
		}
	}

	err = ms.migrationStore.SetMigrated(ctx, anyOrg, true)
	if err != nil {
		return fmt.Errorf("setting migration status: %w", err)
	}
	return nil
}

// newOrgMigration creates a new OrgMigration for the given orgID.
func (ms *MigrationService) newOrgMigration(orgID int64) *OrgMigration {
	return &OrgMigration{
		orgID: orgID,
		log:   ms.log.New("orgID", orgID),

		dialect:           ms.store.GetDialect(),
		dataPath:          ms.cfg.DataPath,
		migrationStore:    ms.migrationStore,
		encryptionService: ms.encryptionService,

		folderHelper: folderHelper{
			dialect:               ms.store.GetDialect(),
			migrationStore:        ms.migrationStore,
			mapActions:            ms.dashboardPermissions.MapActions,
			orgID:                 orgID,
			permissionsMap:        make(map[int64]map[permissionHash]*folder.Folder),
			folderCache:           make(map[int64]*folder.Folder),
			folderPermissionCache: make(map[string][]accesscontrol.ResourcePermission),
		},

		// We deduplicate for case-insensitive matching in MySQL-compatible backend flavours because they use case-insensitive collation.
		seenUIDs:            Deduplicator{set: make(map[string]struct{}), caseInsensitive: ms.store.GetDialect().SupportEngine()},
		silences:            make([]*pb.MeshSilence, 0),
		alertRuleTitleDedup: make(map[string]Deduplicator),
		state: &migmodels.OrgMigrationState{
			OrgID:              orgID,
			MigratedDashboards: make([]*migmodels.DashboardUpgrade, 0),
			MigratedChannels:   make([]*migmodels.ContactPair, 0),
			CreatedFolders:     make([]string, 0),
			Errors:             make([]string, 0),
		},
	}
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

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/grafana/grafana/pkg/infra/db"
	"github.com/grafana/grafana/pkg/infra/kvstore"
	"github.com/grafana/grafana/pkg/infra/log"
	"github.com/grafana/grafana/pkg/services/accesscontrol"
	legacyalerting "github.com/grafana/grafana/pkg/services/alerting"
	legacymodels "github.com/grafana/grafana/pkg/services/alerting/models"
	"github.com/grafana/grafana/pkg/services/auth/identity"
	"github.com/grafana/grafana/pkg/services/dashboards"
	"github.com/grafana/grafana/pkg/services/datasources"
	"github.com/grafana/grafana/pkg/services/folder"
	apimodels "github.com/grafana/grafana/pkg/services/ngalert/api/tooling/definitions"
	"github.com/grafana/grafana/pkg/services/ngalert/models"
	"github.com/grafana/grafana/pkg/services/ngalert/notifier"
	"github.com/grafana/grafana/pkg/services/ngalert/store"
	"github.com/grafana/grafana/pkg/services/org"
	"github.com/grafana/grafana/pkg/services/secrets"
	"github.com/grafana/grafana/pkg/services/user"
	"github.com/grafana/grafana/pkg/setting"
)

// Store is the database abstraction for migration persistence.
type Store interface {
	InsertAlertRules(ctx context.Context, orgID int64, rules []models.AlertRule, provisioned bool) error
	SaveAlertmanagerConfiguration(ctx context.Context, orgID int64, amConfig *apimodels.PostableUserConfig) error
	DeleteMigratedFolders(ctx context.Context, orgID int64) error
	GetDashboard(ctx context.Context, orgID int64, id int64) (*dashboards.Dashboard, error)
	GetAllOrgs(ctx context.Context) ([]*org.OrgDTO, error)
	GetDatasource(ctx context.Context, datasourceID int64, user identity.Requester) (*datasources.DataSource, error)
	GetAlertNotificationUidWithId(ctx context.Context, orgID int64, id int64) (string, error)
	GetNotificationChannels(ctx context.Context, orgID int64) ([]legacymodels.AlertNotification, error)
	GetDashboardAlerts(ctx context.Context, orgID int64) (map[int64][]*legacymodels.Alert, int, error)

	GetDashboardPermissions(ctx context.Context, user identity.Requester, resourceID string) ([]accesscontrol.ResourcePermission, error)
	GetFolderPermissions(ctx context.Context, user identity.Requester, resourceID string) ([]accesscontrol.ResourcePermission, error)
	SetPermissions(ctx context.Context, orgID int64, resourceID string, commands ...accesscontrol.SetResourcePermissionCommand) ([]accesscontrol.ResourcePermission, error)

	GetFolder(ctx context.Context, cmd *folder.GetFolderQuery) (*folder.Folder, error)
	CreateFolder(ctx context.Context, cmd *folder.CreateFolderCommand) (*folder.Folder, error)

	//GetProvenance(ctx context.Context, o models.Provisionable, org int64) (models.Provenance, error)
	//SetProvenance(ctx context.Context, o models.Provisionable, org int64, p models.Provenance) error
	IsProvisioned(ctx context.Context, orgID int64, dashboardUID string) (bool, error)
	UpsertProvenance(ctx context.Context, orgID int64, p models.Provenance, rules []models.AlertRule) error

	IsMigrated(ctx context.Context, orgID int64) (bool, error)
	SetMigrated(ctx context.Context, orgID int64, migrated bool) error
	GetOrgMigrationSummary(ctx context.Context, orgID int64) (*apimodels.OrgMigrationSummary, error)
	SetOrgMigrationSummary(ctx context.Context, orgID int64, summary *apimodels.OrgMigrationSummary) error

	RevertOrg(ctx context.Context, orgID int64) error
	RevertAllOrgs(ctx context.Context) error
}

type migrationStore struct {
	store                db.DB
	cfg                  *setting.Cfg
	log                  log.Logger
	kv                   kvstore.KVStore
	alertingStore        *store.DBstore
	encryptionService    secrets.Service
	dashboardService     dashboards.DashboardService
	folderService        folder.Service
	dataSourceCache      datasources.CacheService
	folderPermissions    accesscontrol.FolderPermissionsService
	dashboardPermissions accesscontrol.DashboardPermissionsService
	orgService           org.Service

	legacyAlertStore             legacyalerting.AlertStore
	dashboardProvisioningService dashboards.DashboardProvisioningService
}

// MigrationStore implements the Store interface.
var _ Store = (*migrationStore)(nil)

func ProvideMigrationStore(
	cfg *setting.Cfg,
	sqlStore db.DB,
	kv kvstore.KVStore,
	alertingStore *store.DBstore,
	encryptionService secrets.Service,
	dashboardService dashboards.DashboardService,
	folderService folder.Service,
	dataSourceCache datasources.CacheService,
	folderPermissions accesscontrol.FolderPermissionsService,
	dashboardPermissions accesscontrol.DashboardPermissionsService,
	orgService org.Service,
	legacyAlertStore legacyalerting.AlertStore,
	dashboardProvisioningService dashboards.DashboardProvisioningService,
) (Store, error) {
	return &migrationStore{
		log:                          log.New("ngalert.migration-store"),
		cfg:                          cfg,
		store:                        sqlStore,
		kv:                           kv,
		alertingStore:                alertingStore,
		encryptionService:            encryptionService,
		dashboardService:             dashboardService,
		folderService:                folderService,
		dataSourceCache:              dataSourceCache,
		folderPermissions:            folderPermissions,
		dashboardPermissions:         dashboardPermissions,
		orgService:                   orgService,
		legacyAlertStore:             legacyAlertStore,
		dashboardProvisioningService: dashboardProvisioningService,
	}, nil
}

// KVNamespace is the kvstore namespace used for the migration status.
const KVNamespace = "ngalert.migration"

// migratedKey is the kvstore key used for the migration status.
const migratedKey = "migrated"

// createdFoldersKey is the kvstore key used for the list of created folder UIDs.
const createdFoldersKey = "createdFolders"

// IsMigrated returns the migration status from the kvstore.
func (ms *migrationStore) IsMigrated(ctx context.Context, orgID int64) (bool, error) {
	kv := kvstore.WithNamespace(ms.kv, orgID, KVNamespace)
	content, exists, err := kv.Get(ctx, migratedKey)
	if err != nil {
		return false, err
	}

	if !exists {
		return false, nil
	}

	return strconv.ParseBool(content)
}

// SetMigrated sets the migration status in the kvstore.
func (ms *migrationStore) SetMigrated(ctx context.Context, orgID int64, migrated bool) error {
	kv := kvstore.WithNamespace(ms.kv, orgID, KVNamespace)
	return kv.Set(ctx, migratedKey, strconv.FormatBool(migrated))
}

// GetOrgMigrationSummary returns a summary of a previous migration.
func (ms *migrationStore) GetOrgMigrationSummary(ctx context.Context, orgID int64) (*apimodels.OrgMigrationSummary, error) {
	kv := kvstore.WithNamespace(ms.kv, orgID, KVNamespace)
	content, exists, err := kv.Get(ctx, createdFoldersKey)
	if err != nil {
		return nil, err
	}

	if !exists {
		return &apimodels.OrgMigrationSummary{OrgID: orgID}, nil
	}

	var summary apimodels.OrgMigrationSummary
	err = json.Unmarshal([]byte(content), &summary)
	if err != nil {
		return nil, err
	}

	return &summary, nil
}

// SetOrgMigrationSummary sets the summary of a previous migration.
func (ms *migrationStore) SetOrgMigrationSummary(ctx context.Context, orgID int64, summary *apimodels.OrgMigrationSummary) error {
	kv := kvstore.WithNamespace(ms.kv, orgID, KVNamespace)
	raw, err := json.Marshal(summary)
	if err != nil {
		return err
	}

	return kv.Set(ctx, createdFoldersKey, string(raw))
}

func (ms *migrationStore) InsertAlertRules(ctx context.Context, orgID int64, rules []models.AlertRule, provisioned bool) error {
	_, err := ms.alertingStore.InsertAlertRules(ctx, rules)
	if err != nil {
		return err
	}
	if provisioned {
		err = ms.UpsertProvenance(ctx, orgID, models.ProvenanceUpgrade, rules)
		if err != nil {
			return err
		}
	}
	return nil
}

func (ms *migrationStore) SaveAlertmanagerConfiguration(ctx context.Context, orgID int64, amConfig *apimodels.PostableUserConfig) error {
	rawAmConfig, err := json.Marshal(amConfig)
	if err != nil {
		return err
	}

	cmd := models.SaveAlertmanagerConfigurationCmd{
		AlertmanagerConfiguration: string(rawAmConfig),
		ConfigurationVersion:      fmt.Sprintf("v%d", models.AlertConfigurationVersion),
		Default:                   false,
		OrgID:                     orgID,
		LastApplied:               0,
	}
	return ms.alertingStore.SaveAlertmanagerConfiguration(ctx, &cmd)
}

// revertPermissions are the permissions required for the background user to revert the migration.
var revertPermissions = []accesscontrol.Permission{
	{Action: dashboards.ActionFoldersDelete, Scope: dashboards.ScopeFoldersAll},
}

func (ms *migrationStore) RevertOrg(ctx context.Context, orgID int64) error {
	return ms.store.WithTransactionalDbSession(ctx, func(sess *db.Session) error {
		if _, err := sess.Exec("DELETE FROM alert_rule WHERE org_id = ?", orgID); err != nil {
			return err
		}

		if _, err := sess.Exec("DELETE FROM alert_rule_version WHERE rule_org_id = ?", orgID); err != nil { //TODO rule_org_id
			return err
		}

		if err := ms.DeleteMigratedFolders(ctx, orgID); err != nil {
			return err
		}

		if _, err := sess.Exec("DELETE FROM alert_configuration WHERE org_id = ?", orgID); err != nil {
			return err
		}

		if _, err := sess.Exec("DELETE FROM ngalert_configuration WHERE org_id = ?", orgID); err != nil {
			return err
		}

		if _, err := sess.Exec("DELETE FROM alert_instance WHERE rule_org_id = ?", orgID); err != nil { //TODO rule_org_id
			return err
		}

		if _, err := sess.Exec("DELETE FROM kv_store WHERE namespace = ? AND org_id = ?", notifier.KVNamespace, orgID); err != nil {
			return err
		}

		if _, err := sess.Exec("DELETE FROM kv_store WHERE namespace = ? AND org_id = ?", KVNamespace, orgID); err != nil {
			return err
		}

		files, err := filepath.Glob(filepath.Join(ms.cfg.DataPath, "alerting", strconv.FormatInt(orgID, 10), "silences"))
		if err != nil {
			return err
		}
		for _, f := range files {
			if err := os.Remove(f); err != nil {
				ms.log.Error("alert migration error: failed to remove silence file", "file", f, "err", err)
			}
		}

		return nil
	})
}

func (ms *migrationStore) RevertAllOrgs(ctx context.Context) error {
	return ms.store.WithTransactionalDbSession(ctx, func(sess *db.Session) error {
		if _, err := sess.Exec("DELETE FROM alert_rule"); err != nil {
			return err
		}

		if _, err := sess.Exec("DELETE FROM alert_rule_version"); err != nil { //TODO rule_org_id
			return err
		}

		orgs, err := ms.GetAllOrgs(ctx)
		if err != nil {
			return fmt.Errorf("can't get org list: %w", err)
		}
		for _, o := range orgs {
			if err := ms.DeleteMigratedFolders(ctx, o.ID); err != nil {
				return err
			}
		}

		if _, err := sess.Exec("DELETE FROM alert_configuration"); err != nil {
			return err
		}

		if _, err := sess.Exec("DELETE FROM ngalert_configuration"); err != nil {
			return err
		}

		if _, err := sess.Exec("DELETE FROM alert_instance"); err != nil { //TODO rule_org_id
			return err
		}

		if _, err := sess.Exec("DELETE FROM kv_store WHERE namespace = ?", notifier.KVNamespace); err != nil {
			return err
		}

		if _, err := sess.Exec("DELETE FROM kv_store WHERE namespace = ?", KVNamespace); err != nil {
			return err
		}

		files, err := filepath.Glob(filepath.Join(ms.cfg.DataPath, "alerting", "*", "silences"))
		if err != nil {
			return err
		}
		for _, f := range files {
			if err := os.Remove(f); err != nil {
				ms.log.Error("alert migration error: failed to remove silence file", "file", f, "err", err)
			}
		}

		return nil
	})
}

func (ms *migrationStore) DeleteMigratedFolders(ctx context.Context, orgID int64) error {
	summary, err := ms.GetOrgMigrationSummary(ctx, orgID)
	if err != nil {
		return err
	}
	if len(summary.CreatedFolders) == 0 {
		return nil
	}
	usr := accesscontrol.BackgroundUser("ngalert_migration_revert", orgID, org.RoleAdmin, revertPermissions)
	for _, folderUID := range summary.CreatedFolders {
		cmd := folder.DeleteFolderCommand{
			UID:          folderUID,
			OrgID:        orgID,
			SignedInUser: usr.(*user.SignedInUser),
		}
		err := ms.folderService.Delete(ctx, &cmd) // Also handles permissions and other related entities.
		if err != nil {
			return err
		}
	}
	return nil
}

func (ms *migrationStore) GetDashboard(ctx context.Context, orgID int64, id int64) (*dashboards.Dashboard, error) {
	return ms.dashboardService.GetDashboard(ctx, &dashboards.GetDashboardQuery{ID: id, OrgID: orgID})
}

func (ms *migrationStore) GetAllOrgs(ctx context.Context) ([]*org.OrgDTO, error) {
	orgQuery := &org.SearchOrgsQuery{}
	return ms.orgService.Search(ctx, orgQuery)
}

func (ms *migrationStore) GetDatasource(ctx context.Context, datasourceID int64, user identity.Requester) (*datasources.DataSource, error) {
	return ms.dataSourceCache.GetDatasource(ctx, datasourceID, user, false)
}

func (ms *migrationStore) GetAlertNotificationUidWithId(ctx context.Context, orgID int64, id int64) (string, error) {
	cmd := legacymodels.GetAlertNotificationUidQuery{
		ID:    id,
		OrgID: orgID,
	}
	return ms.legacyAlertStore.GetAlertNotificationUidWithId(ctx, &cmd)
}

// GetNotificationChannels returns all channels for this org.
func (ms *migrationStore) GetNotificationChannels(ctx context.Context, orgID int64) ([]legacymodels.AlertNotification, error) {
	q := `
	SELECT id,
		org_id,
		uid,
		name,
		type,
		disable_resolve_message,
		is_default,
		settings,
		secure_settings,
        send_reminder,
		frequency
	FROM
		alert_notification
	WHERE
		org_id = ?
	ORDER BY
	    is_default DESC
	`
	var alertNotifications []legacymodels.AlertNotification
	err := ms.store.WithDbSession(ctx, func(sess *db.Session) error {
		return sess.SQL(q, orgID).Find(&alertNotifications)
	})
	if err != nil {
		return nil, err
	}

	return alertNotifications, nil
}

// GetDashboardAlerts loads all legacy dashboard alerts for the given org mapped by dashboard id.
func (ms *migrationStore) GetDashboardAlerts(ctx context.Context, orgID int64) (map[int64][]*legacymodels.Alert, int, error) {
	var dashAlerts []*legacymodels.Alert
	err := ms.store.WithDbSession(ctx, func(sess *db.Session) error {
		return sess.SQL("select * from alert WHERE org_id = ?", orgID).Find(&dashAlerts)
	})
	if err != nil {
		return nil, 0, fmt.Errorf("could not load alerts: %w", err)
	}

	mappedAlerts := make(map[int64][]*legacymodels.Alert)
	for _, alert := range dashAlerts {
		mappedAlerts[alert.DashboardID] = append(mappedAlerts[alert.DashboardID], alert)
	}
	return mappedAlerts, len(dashAlerts), nil
}

func (ms *migrationStore) GetDashboardPermissions(ctx context.Context, user identity.Requester, resourceID string) ([]accesscontrol.ResourcePermission, error) {
	return ms.dashboardPermissions.GetPermissions(ctx, user, resourceID)
}

func (ms *migrationStore) GetFolderPermissions(ctx context.Context, user identity.Requester, resourceID string) ([]accesscontrol.ResourcePermission, error) {
	return ms.folderPermissions.GetPermissions(ctx, user, resourceID)
}

func (ms *migrationStore) GetFolder(ctx context.Context, cmd *folder.GetFolderQuery) (*folder.Folder, error) {
	return ms.folderService.Get(ctx, cmd)
}

func (ms *migrationStore) CreateFolder(ctx context.Context, cmd *folder.CreateFolderCommand) (*folder.Folder, error) {
	return ms.folderService.Create(ctx, cmd)
}

func (ms *migrationStore) SetPermissions(ctx context.Context, orgID int64, resourceID string, commands ...accesscontrol.SetResourcePermissionCommand) ([]accesscontrol.ResourcePermission, error) {
	return ms.folderPermissions.SetPermissions(ctx, orgID, resourceID, commands...)
}

func (ms *migrationStore) IsProvisioned(ctx context.Context, orgID int64, dashboardUID string) (bool, error) {
	info, err := ms.dashboardProvisioningService.GetProvisionedDashboardDataByDashboardUID(ctx, orgID, dashboardUID)
	if err != nil {
		if errors.Is(err, dashboards.ErrProvisionedDashboardNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("failed to get provisioned status: %w", err)
	}

	return info != nil, nil
}

func (ms *migrationStore) UpsertProvenance(ctx context.Context, orgID int64, p models.Provenance, rules []models.AlertRule) error {
	var result []models.Provisionable
	for _, r := range rules {
		result = append(result, &r)
	}
	return ms.alertingStore.UpsertProvenance(ctx, orgID, p, result...)
}

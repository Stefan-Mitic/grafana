package store

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/grafana/grafana/pkg/services/ngalert/store"
	"github.com/grafana/grafana/pkg/services/org"
	"github.com/grafana/grafana/pkg/services/secrets"
	"github.com/grafana/grafana/pkg/services/user"
	"github.com/grafana/grafana/pkg/setting"
)

// Store is the database abstraction for migration persistence.
type Store interface {
	InsertAlertRules(ctx context.Context, l log.Logger, rules []models.AlertRule) error
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

	IsMigrated(ctx context.Context, orgID int64) (bool, error)
	SetMigrated(ctx context.Context, orgID int64, migrated bool) error
	GetCreatedFolders(ctx context.Context, orgID int64) (map[int64][]string, error)
	SetCreatedFolders(ctx context.Context, orgID int64, folderUids map[int64][]string) error
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

	legacyAlertStore legacyalerting.AlertStore
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
) (Store, error) {
	return &migrationStore{
		log:                  log.New("ngalert.migration-store"),
		cfg:                  cfg,
		store:                sqlStore,
		kv:                   kv,
		alertingStore:        alertingStore,
		encryptionService:    encryptionService,
		dashboardService:     dashboardService,
		folderService:        folderService,
		dataSourceCache:      dataSourceCache,
		folderPermissions:    folderPermissions,
		dashboardPermissions: dashboardPermissions,
		orgService:           orgService,
		legacyAlertStore:     legacyAlertStore,
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

// GetCreatedFolders returns a map of orgID to list of folder UIDs that were created by the migration from the kvstore.
func (ms *migrationStore) GetCreatedFolders(ctx context.Context, orgID int64) (map[int64][]string, error) {
	kv := kvstore.WithNamespace(ms.kv, orgID, KVNamespace)
	content, exists, err := kv.Get(ctx, createdFoldersKey)
	if err != nil {
		return nil, err
	}

	if !exists {
		return make(map[int64][]string), nil
	}

	var folderUids map[int64][]string
	err = json.Unmarshal([]byte(content), &folderUids)
	if err != nil {
		return nil, err
	}

	return folderUids, nil
}

// SetCreatedFolders sets the map of orgID to list of folder UIDs that were created by the migration in the kvstore.
func (ms *migrationStore) SetCreatedFolders(ctx context.Context, orgID int64, folderUids map[int64][]string) error {
	kv := kvstore.WithNamespace(ms.kv, orgID, KVNamespace)
	raw, err := json.Marshal(folderUids)
	if err != nil {
		return err
	}

	return kv.Set(ctx, createdFoldersKey, string(raw))
}

func (ms *migrationStore) InsertAlertRules(ctx context.Context, l log.Logger, rules []models.AlertRule) error {
	ms.log.Info("Inserting migrated alert rules", "count", len(rules))
	_, err := ms.alertingStore.InsertAlertRules(ctx, rules)
	if err != nil {
		return err
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

func (ms *migrationStore) DeleteMigratedFolders(ctx context.Context, orgID int64) error {
	createdFolders, err := ms.GetCreatedFolders(ctx, orgID)
	if err != nil {
		return err
	}
	for orgId, folderUIDs := range createdFolders {
		usr := accesscontrol.BackgroundUser("ngalert_migration_revert", orgID, org.RoleAdmin, revertPermissions)

		for _, folderUID := range folderUIDs {
			cmd := folder.DeleteFolderCommand{
				UID:          folderUID,
				OrgID:        orgId,
				SignedInUser: usr.(*user.SignedInUser),
			}
			err := ms.folderService.Delete(ctx, &cmd) // Also handles permissions and other related entities.
			if err != nil {
				return err
			}
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

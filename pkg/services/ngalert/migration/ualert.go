package migration

import (
	"context"
	"errors"
	"fmt"
	"strings"

	pb "github.com/prometheus/alertmanager/silence/silencepb"

	"github.com/grafana/grafana/pkg/infra/db"
	"github.com/grafana/grafana/pkg/infra/log"
	"github.com/grafana/grafana/pkg/services/accesscontrol"
	"github.com/grafana/grafana/pkg/services/dashboards"
	"github.com/grafana/grafana/pkg/services/datasources"
	"github.com/grafana/grafana/pkg/services/folder"
	"github.com/grafana/grafana/pkg/services/ngalert/models"
	"github.com/grafana/grafana/pkg/services/org"
	"github.com/grafana/grafana/pkg/services/secrets"
	"github.com/grafana/grafana/pkg/services/sqlstore/migrator"
	"github.com/grafana/grafana/pkg/setting"
	"github.com/grafana/grafana/pkg/util"
)

// DASHBOARD_FOLDER is the format used to generate the folder name for migrated dashboards with custom permissions.
const DASHBOARD_FOLDER = "%s Alerts - %s"

// MaxFolderName is the maximum length of the folder name generated using DASHBOARD_FOLDER format
const MaxFolderName = 255

// It is defined in pkg/expr/service.go as "DatasourceType"
const expressionDatasourceUID = "__expr__"

type migration struct {
	log     log.Logger
	dialect migrator.Dialect
	cfg     *setting.Cfg

	info                 InfoStore
	store                db.DB
	ruleStore            RuleStore
	alertingStore        AlertingStore
	encryptionService    secrets.Service
	dashboardService     dashboards.DashboardService
	folderService        folder.Service
	dsCacheService       datasources.CacheService
	folderPermissions    accesscontrol.FolderPermissionsService
	dashboardPermissions accesscontrol.DashboardPermissionsService
	orgService           org.Service

	createdOrgFolderUids map[int64][]string
}

func newMigration(
	log log.Logger,
	cfg *setting.Cfg,
	info InfoStore,
	store db.DB,
	ruleStore RuleStore,
	alertingStore AlertingStore,
	encryptionService secrets.Service,
	dashboardService dashboards.DashboardService,
	folderService folder.Service,
	dsCacheService datasources.CacheService,
	folderPermissions accesscontrol.FolderPermissionsService,
	dashboardPermissions accesscontrol.DashboardPermissionsService,
	orgService org.Service,
) *migration {
	return &migration{
		log:                  log,
		dialect:              store.GetDialect(),
		cfg:                  cfg,
		info:                 info,
		store:                store,
		ruleStore:            ruleStore,
		alertingStore:        alertingStore,
		encryptionService:    encryptionService,
		dashboardService:     dashboardService,
		folderService:        folderService,
		dsCacheService:       dsCacheService,
		folderPermissions:    folderPermissions,
		dashboardPermissions: dashboardPermissions,
		orgService:           orgService,

		createdOrgFolderUids: make(map[int64][]string),
	}
}

// orgMigration is a helper struct for migrating alerts for a single org. It contains state, services, and caches.
type orgMigration struct {
	orgID int64
	log   log.Logger

	dialect           migrator.Dialect
	store             db.DB
	dataPath          string
	dsCacheService    datasources.CacheService
	encryptionService secrets.Service

	folderHelper folderHelper

	seenUIDs            deduplicator
	silences            []*pb.MeshSilence
	alertRuleTitleDedup map[string]deduplicator // Folder -> deduplicator (Title).
}

// newOrgMigration creates a new orgMigration for the given orgID.
func newOrgMigration(m *migration, orgID int64) *orgMigration {
	return &orgMigration{
		orgID: orgID,
		log:   m.log.New("orgID", orgID),

		store:             m.store,
		dialect:           m.dialect,
		dataPath:          m.cfg.DataPath,
		dsCacheService:    m.dsCacheService,
		encryptionService: m.encryptionService,

		folderHelper: folderHelper{
			info:                  m.info,
			dialect:               m.dialect,
			orgID:                 orgID,
			folderService:         m.folderService,
			folderPermissions:     m.folderPermissions,
			dashboardPermissions:  m.dashboardPermissions,
			permissionsMap:        make(map[int64]map[permissionHash]*folder.Folder),
			folderCache:           make(map[int64]*folder.Folder),
			folderPermissionCache: make(map[string][]accesscontrol.ResourcePermission),
		},

		// We deduplicate for case-insensitive matching in MySQL-compatible backend flavours because they use case-insensitive collation.
		seenUIDs:            deduplicator{set: make(map[string]struct{}), caseInsensitive: m.dialect.SupportEngine()},
		silences:            make([]*pb.MeshSilence, 0),
		alertRuleTitleDedup: make(map[string]deduplicator),
	}
}

func (m *migration) migrateOrg(ctx context.Context, orgID int64) error {
	om := newOrgMigration(m, orgID)
	rules := make(map[*models.AlertRule][]uidOrID)
	om.log.Info("migrating alerts for organisation")

	mappedAlerts, err := m.slurpDashAlerts(ctx, om.log, orgID)
	if err != nil {
		return fmt.Errorf("failed to get alerts for org %d: %w", orgID, err)
	}

	for dashID, alerts := range mappedAlerts {
		dash, err := m.dashboardService.GetDashboard(ctx, &dashboards.GetDashboardQuery{ID: dashID, OrgID: orgID})
		if err != nil {
			if errors.Is(err, dashboards.ErrDashboardNotFound) {
				om.log.Warn(fmt.Sprintf("%d alerts found but have an unknown dashboard, skipping", len(alerts)), "dashboardID", dashID)
				continue
			}
			return fmt.Errorf("failed to get dashboard [ID: %d]: %w", dashID, err)
		}
		l := om.log.New("dashboardTitle", dash.Title, "dashboardUID", dash.UID)

		f, err := om.folderHelper.getOrCreateMigratedFolder(ctx, l, dash)
		if err != nil {
			return fmt.Errorf("failed to get or create folder for dashboard %s [ID: %d]: %w", dash.Title, dash.ID, err)
		}

		for _, da := range alerts {
			l = l.New("ruleID", da.ID, "ruleName", da.Name)
			alertRule, channels, err := om.migrateAlert(ctx, l, da, dash, f)
			if err != nil {
				return fmt.Errorf("failed to migrate alert %s [ID: %d] on dashboard %s [ID: %d]: %w", da.Name, da.ID, dash.Title, dash.ID, err)
			}
			rules[alertRule] = channels
		}
	}

	amConfig, err := om.setupAlertmanagerConfigs(ctx, rules)
	if err != nil {
		return err
	}

	// Validate the alertmanager configuration produced, this gives a chance to catch bad configuration at migration time.
	// Validation between legacy and unified alerting can be different (e.g. due to bug fixes) so this would fail the migration in that case.
	if err := om.validateAlertmanagerConfig(amConfig); err != nil {
		return fmt.Errorf("failed to validate AlertmanagerConfig in orgId %d: %w", orgID, err)
	}

	err = m.insertRules(ctx, orgID, rules)
	if err != nil {
		return err
	}

	if err := om.writeSilencesFile(); err != nil {
		m.log.Error("Failed to write silence file", "err", err)
	}

	m.log.Info("Writing alertmanager config", "orgID", orgID, "receivers", len(amConfig.AlertmanagerConfig.Receivers), "routes", len(amConfig.AlertmanagerConfig.Route.Routes))
	if err := m.writeAlertmanagerConfig(ctx, orgID, amConfig); err != nil {
		return fmt.Errorf("failed to write AlertmanagerConfig in orgId %d: %w", orgID, err)
	}

	if len(om.folderHelper.createdFolders) > 0 {
		folderUids := make([]string, 0, len(om.folderHelper.createdFolders))
		for _, f := range om.folderHelper.createdFolders {
			folderUids = append(folderUids, f.UID)
		}
		m.createdOrgFolderUids[om.orgID] = folderUids
	}

	return nil
}

// Exec executes the migration.
func (m *migration) Exec(ctx context.Context) error {
	orgQuery := &org.SearchOrgsQuery{}
	orgs, err := m.orgService.Search(ctx, orgQuery)
	if err != nil {
		return fmt.Errorf("can't get org list: %w", err)
	}

	for _, o := range orgs {
		err := m.migrateOrg(ctx, o.ID)
		if err != nil {
			return fmt.Errorf("failed to migrate org %d: %w", o.ID, err)
		}
	}

	err = m.info.setCreatedFolders(ctx, m.createdOrgFolderUids)
	if err != nil {
		return err
	}

	return nil
}

// deduplicator is a wrapper around map[string]struct{} and util.GenerateShortUID() which aims help maintain and generate
// unique strings (such as uids or titles). if caseInsensitive is true, all uniqueness is determined in a
// case-insensitive manner. if maxLen is greater than 0, all strings will be truncated to maxLen before being checked in
// contains and dedup will always return a string of length maxLen or less.
type deduplicator struct {
	set             map[string]struct{}
	caseInsensitive bool
	maxLen          int
}

// contains checks whether the given string has already been seen by this deduplicator.
func (s *deduplicator) contains(u string) bool {
	dedup := u
	if s.caseInsensitive {
		dedup = strings.ToLower(dedup)
	}
	if s.maxLen > 0 && len(dedup) > s.maxLen {
		dedup = dedup[:s.maxLen]
	}
	_, seen := s.set[dedup]
	return seen
}

// deduplicate returns a unique string based on the given string by appending a uuid to it. Will truncate the given string if
// the resulting string would be longer than maxLen.
func (s *deduplicator) deduplicate(dedup string) (string, error) {
	uid := util.GenerateShortUID()
	if s.maxLen > 0 && len(dedup)+1+len(uid) > s.maxLen {
		trunc := s.maxLen - 1 - len(uid)
		dedup = dedup[:trunc]
	}

	return dedup + "_" + uid, nil
}

// add adds the given string to the deduplicator.
func (s *deduplicator) add(uid string) {
	dedup := uid
	if s.caseInsensitive {
		dedup = strings.ToLower(dedup)
	}
	s.set[dedup] = struct{}{}
}

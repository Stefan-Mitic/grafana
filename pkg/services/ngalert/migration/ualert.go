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
	"github.com/grafana/grafana/pkg/services/ngalert/store"
	"github.com/grafana/grafana/pkg/services/org"
	"github.com/grafana/grafana/pkg/services/secrets"
	"github.com/grafana/grafana/pkg/services/sqlstore/migrator"
	"github.com/grafana/grafana/pkg/util"
)

// DASHBOARD_FOLDER is the format used to generate the folder name for migrated dashboards with custom permissions.
const DASHBOARD_FOLDER = "%s Alerts - %s"

// MaxFolderName is the maximum length of the folder name generated using DASHBOARD_FOLDER format
const MaxFolderName = 255

// It is defined in pkg/expr/service.go as "DatasourceType"
const expressionDatasourceUID = "__expr__"

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
	createdFolderUids   []string
}

// newOrgMigration creates a new orgMigration for the given orgID.
func (ms *MigrationService) newOrgMigration(orgID int64) *orgMigration {
	return &orgMigration{
		orgID: orgID,
		log:   ms.log.New("orgID", orgID),

		store:             ms.store,
		dialect:           ms.store.GetDialect(),
		dataPath:          ms.cfg.DataPath,
		dsCacheService:    ms.dataSourceCache,
		encryptionService: ms.encryptionService,

		folderHelper: folderHelper{
			info:                  ms.info,
			dialect:               ms.store.GetDialect(),
			orgID:                 orgID,
			folderService:         ms.folderService,
			folderPermissions:     ms.folderPermissions,
			dashboardPermissions:  ms.dashboardPermissions,
			permissionsMap:        make(map[int64]map[permissionHash]*folder.Folder),
			folderCache:           make(map[int64]*folder.Folder),
			folderPermissionCache: make(map[string][]accesscontrol.ResourcePermission),
		},

		// We deduplicate for case-insensitive matching in MySQL-compatible backend flavours because they use case-insensitive collation.
		seenUIDs:            deduplicator{set: make(map[string]struct{}), caseInsensitive: ms.store.GetDialect().SupportEngine()},
		silences:            make([]*pb.MeshSilence, 0),
		alertRuleTitleDedup: make(map[string]deduplicator),
	}
}

func (ms *MigrationService) migrateOrg(ctx context.Context, orgID int64) (*orgMigration, error) {
	om := ms.newOrgMigration(orgID)
	om.log.Info("migrating alerts for organisation")

	mappedAlerts, err := ms.slurpDashAlerts(ctx, om.log, orgID)
	if err != nil {
		return nil, fmt.Errorf("failed to get alerts for org %d: %w", orgID, err)
	}

	for dashID, alerts := range mappedAlerts {
		dash, err := ms.dashboardService.GetDashboard(ctx, &dashboards.GetDashboardQuery{ID: dashID, OrgID: orgID})
		if err != nil {
			if errors.Is(err, dashboards.ErrDashboardNotFound) {
				om.log.Warn(fmt.Sprintf("%d alerts found but have an unknown dashboard, skipping", len(alerts)), "dashboardID", dashID)
				continue
			}
			return nil, fmt.Errorf("failed to get dashboard [ID: %d]: %w", dashID, err)
		}
		l := om.log.New("dashboardTitle", dash.Title, "dashboardUID", dash.UID)

		f, err := om.folderHelper.getOrCreateMigratedFolder(ctx, l, dash)
		if err != nil {
			return nil, fmt.Errorf("failed to get or create folder for dashboard %s [ID: %d]: %w", dash.Title, dash.ID, err)
		}

		// Here we ensure that the alert rule title is unique within the folder.
		if _, ok := om.alertRuleTitleDedup[f.UID]; !ok {
			om.alertRuleTitleDedup[f.UID] = deduplicator{
				set:             make(map[string]struct{}),
				caseInsensitive: om.dialect.SupportEngine(),
				maxLen:          store.AlertDefinitionMaxTitleLength,
			}
		}
		dedupSet := om.alertRuleTitleDedup[f.UID]

		rules := make([]models.AlertRule, 0, len(alerts))
		for _, da := range alerts {
			l := l.New("ruleID", da.ID, "ruleName", da.Name)
			alertRule, err := ms.MigrateAlert(ctx, l, da, dash, f)
			if err != nil {
				return nil, fmt.Errorf("failed to migrate alert %s [ID: %d] on dashboard %s [ID: %d]: %w", da.Name, da.ID, dash.Title, dash.ID, err)
			}

			if dedupSet.contains(alertRule.Title) {
				dedupedName := dedupSet.deduplicate(alertRule.Title)
				l.Warn("duplicate alert rule name detected, renaming", "old_name", alertRule.Title, "new_name", dedupedName)
				alertRule.Title = dedupedName
			}
			dedupSet.add(alertRule.Title)
			om.silences = append(om.silences, createSilences(l, da, alertRule)...)
			rules = append(rules, *alertRule)
		}

		if len(rules) > 0 {
			err = ms.insertRules(ctx, om.log, rules)
			if err != nil {
				return nil, err
			}
		}
	}

	amConfig, err := om.setupAlertmanagerConfigs(ctx)
	if err != nil {
		return nil, err
	}

	// Validate the alertmanager configuration produced, this gives a chance to catch bad configuration at migration time.
	// Validation between legacy and unified alerting can be different (e.g. due to bug fixes) so this would fail the migration in that case.
	if err := om.validateAlertmanagerConfig(amConfig); err != nil {
		return nil, fmt.Errorf("failed to validate AlertmanagerConfig in orgId %d: %w", orgID, err)
	}

	if err := om.writeSilencesFile(); err != nil {
		ms.log.Error("Failed to write silence file", "err", err)
	}

	ms.log.Info("Writing alertmanager config", "orgID", orgID, "receivers", len(amConfig.AlertmanagerConfig.Receivers), "routes", len(amConfig.AlertmanagerConfig.Route.Routes))
	if err := ms.writeAlertmanagerConfig(ctx, orgID, amConfig); err != nil {
		return nil, fmt.Errorf("failed to write AlertmanagerConfig in orgId %d: %w", orgID, err)
	}

	om.createdFolderUids = make([]string, 0, len(om.folderHelper.createdFolders))
	for _, f := range om.folderHelper.createdFolders {
		om.createdFolderUids = append(om.createdFolderUids, f.UID)
	}

	return om, nil
}

// Exec executes the migration.
func (ms *MigrationService) Exec(ctx context.Context) error {
	orgQuery := &org.SearchOrgsQuery{}
	orgs, err := ms.orgService.Search(ctx, orgQuery)
	if err != nil {
		return fmt.Errorf("can't get org list: %w", err)
	}

	createdOrgFolderUids := make(map[int64][]string)
	for _, o := range orgs {
		om, err := ms.migrateOrg(ctx, o.ID)
		if err != nil {
			return fmt.Errorf("failed to migrate org %d: %w", o.ID, err)
		}
		if len(om.createdFolderUids) > 0 {
			createdOrgFolderUids[o.ID] = om.createdFolderUids
		}
	}

	err = ms.info.setCreatedFolders(ctx, createdOrgFolderUids)
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
func (s *deduplicator) deduplicate(dedup string) string {
	uid := util.GenerateShortUID()
	if s.maxLen > 0 && len(dedup)+1+len(uid) > s.maxLen {
		trunc := s.maxLen - 1 - len(uid)
		dedup = dedup[:trunc]
	}

	return dedup + "_" + uid
}

// add adds the given string to the deduplicator.
func (s *deduplicator) add(uid string) {
	dedup := uid
	if s.caseInsensitive {
		dedup = strings.ToLower(dedup)
	}
	s.set[dedup] = struct{}{}
}

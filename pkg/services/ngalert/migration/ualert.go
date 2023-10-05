package migration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	pb "github.com/prometheus/alertmanager/silence/silencepb"
	"github.com/prometheus/common/model"

	"github.com/grafana/grafana/pkg/infra/log"
	"github.com/grafana/grafana/pkg/services/accesscontrol"
	legacymodels "github.com/grafana/grafana/pkg/services/alerting/models"
	"github.com/grafana/grafana/pkg/services/dashboards"
	"github.com/grafana/grafana/pkg/services/folder"
	apiModels "github.com/grafana/grafana/pkg/services/ngalert/api/tooling/definitions"
	migrationStore "github.com/grafana/grafana/pkg/services/ngalert/migration/store"
	"github.com/grafana/grafana/pkg/services/ngalert/models"
	"github.com/grafana/grafana/pkg/services/ngalert/store"
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
	migrationStore    migrationStore.Store
	encryptionService secrets.Service

	dataPath string

	folderHelper folderHelper

	seenUIDs            deduplicator
	silences            []*pb.MeshSilence
	alertRuleTitleDedup map[string]deduplicator // Folder -> deduplicator (Title).

	summary *apiModels.OrgMigrationSummary
}

func newDashboardUpgrade(dashboardId int64) *apiModels.DashboardUpgrade {
	return &apiModels.DashboardUpgrade{
		MigratedAlerts: nil,
		DashboardID:    dashboardId,
		DashboardUID:   "",
		DashboardName:  "",
		FolderUID:      "",
		FolderName:     "",
		NewFolderUID:   "",
		NewFolderName:  "",
		Provisioned:    false,
		Errors:         nil,
		Warnings:       nil,
	}
}

func attachAlertRule(pair *apiModels.AlertPair, rule *models.AlertRule) {
	pair.AlertRule = &apiModels.AlertRuleUpgrade{
		Modified:     false,
		UID:          rule.UID,
		Title:        rule.Title,
		DashboardUID: rule.DashboardUID,
		PanelID:      rule.PanelID,
		NoDataState:  apiModels.NoDataState(rule.NoDataState),
		ExecErrState: apiModels.ExecutionErrorState(rule.ExecErrState),
		For:          model.Duration(rule.For),
		Annotations:  rule.Annotations,
		Labels:       rule.Labels,
		IsPaused:     rule.IsPaused,
	}
}

func newContactPair(channel *legacymodels.AlertNotification, contactPoint *apiModels.PostableApiReceiver, provisioned bool, err error) *apiModels.ContactPair {
	pair := &apiModels.ContactPair{
		LegacyChannel: &apiModels.LegacyChannel{
			Modified:              false,
			ID:                    channel.ID,
			UID:                   channel.UID,
			Name:                  channel.Name,
			Type:                  channel.Type,
			SendReminder:          channel.SendReminder,
			DisableResolveMessage: channel.DisableResolveMessage,
			Frequency:             model.Duration(channel.Frequency),
			IsDefault:             channel.IsDefault,
		},
		Provisioned: provisioned, //TODO: implement
	}
	if contactPoint != nil {
		pair.ContactPointUpgrade = &apiModels.ContactPointUpgrade{
			Modified:              false,
			Name:                  contactPoint.Name,
			UID:                   contactPoint.GrafanaManagedReceivers[0].UID,
			Type:                  contactPoint.GrafanaManagedReceivers[0].Type,
			DisableResolveMessage: contactPoint.GrafanaManagedReceivers[0].DisableResolveMessage,
		}
	}
	if err != nil {
		pair.Error = err.Error()
	}
	return pair
}

// newOrgMigration creates a new orgMigration for the given orgID.
func (ms *MigrationService) newOrgMigration(orgID int64) *orgMigration {
	return &orgMigration{
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
		seenUIDs:            deduplicator{set: make(map[string]struct{}), caseInsensitive: ms.store.GetDialect().SupportEngine()},
		silences:            make([]*pb.MeshSilence, 0),
		alertRuleTitleDedup: make(map[string]deduplicator),
		summary: &apiModels.OrgMigrationSummary{
			OrgID:              orgID,
			MigratedDashboards: make([]*apiModels.DashboardUpgrade, 0),
			MigratedChannels:   make([]*apiModels.ContactPair, 0),
			CreatedFolders:     make([]string, 0),
			Errors:             make([]string, 0),
		},
	}
}

var ActiveMigrationError = errors.New("organization has already been migrated")

// MigrateAlert migrates a single dashboard alert from legacy alerting to unified alerting.
func (om *orgMigration) migrateAlert(ctx context.Context, l log.Logger, alert *legacymodels.Alert, dash *dashboards.Dashboard, f *folder.Folder) (*models.AlertRule, error) {
	l.Debug("migrating alert rule to Unified Alerting")
	rawSettings, err := json.Marshal(alert.Settings)
	if err != nil {
		return nil, fmt.Errorf("failed to get settings: %w", err)
	}
	var parsedSettings dashAlertSettings
	err = json.Unmarshal(rawSettings, &parsedSettings)
	if err != nil {
		return nil, fmt.Errorf("failed to parse settings: %w", err)
	}
	newCond, err := transConditions(ctx, parsedSettings, alert.OrgID, om.migrationStore)
	if err != nil {
		return nil, fmt.Errorf("failed to transform conditions: %w", err)
	}

	channels := om.extractChannelUIDs(ctx, l, alert.OrgID, parsedSettings)

	rule, err := makeAlertRule(l, *newCond, alert, dash, f.UID, channels)
	if err != nil {
		return nil, fmt.Errorf("failed to make alert rule: %w", err)
	}

	return rule, nil
}

func (om *orgMigration) migrateDashboard(ctx context.Context, dashID int64, alerts []*legacymodels.Alert) (*apiModels.DashboardUpgrade, error) {
	du := newDashboardUpgrade(dashID)
	dash, err := om.migrationStore.GetDashboard(ctx, om.orgID, dashID)
	if err != nil {
		return du, fmt.Errorf("failed to get dashboard: %w", err)
	}
	du.SetDashboard(dash.UID, dash.Title)
	l := om.log.New("dashboardTitle", dash.Title, "dashboardUID", dash.UID)

	provisioned, err := om.migrationStore.IsProvisioned(ctx, om.orgID, dash.UID)
	if err != nil {
		l.Warn("failed to get provisioned status for dashboard", "error", err)
		du.Warnings = append(du.Warnings, fmt.Errorf("failed to get provisioned status: %w", err).Error())
	}
	du.Provisioned = provisioned

	// dashFolder can be nil if the dashboard's folder is missing and the migrated folder is general alerting.
	dashFolder, migratedFolder, err := om.folderHelper.getOrCreateMigratedFolder(ctx, l, dash)
	if dashFolder != nil {
		du.SetFolder(dashFolder.UID, dashFolder.Title)
	}
	if err != nil {
		return du, fmt.Errorf("failed to get or create folder for new alert rule: %w", err)
	}
	du.SetNewFolder(migratedFolder.UID, migratedFolder.Title)
	l = l.New("newFolderUID", migratedFolder.UID, "newFolderName", migratedFolder.Title)
	if dashFolder != nil {
		l = l.New("folderUID", dashFolder.UID, "folderName", dashFolder.Title)
	}

	if dashFolder == nil {
		du.Warnings = append(du.Warnings, "dashboard alerts moved to general alerting folder during upgrade: original folder not found")
	} else if dashFolder.UID != migratedFolder.UID {
		du.Warnings = append(du.Warnings, "dashboard alerts moved to new folder during upgrade: folder permission changes were needed")
	}

	// Here we ensure that the alert rule title is unique within the folder.
	if _, ok := om.alertRuleTitleDedup[migratedFolder.UID]; !ok {
		om.alertRuleTitleDedup[migratedFolder.UID] = deduplicator{
			set:             make(map[string]struct{}),
			caseInsensitive: om.dialect.SupportEngine(),
			maxLen:          store.AlertDefinitionMaxTitleLength,
		}
	}
	dedupSet := om.alertRuleTitleDedup[migratedFolder.UID]

	rules := make([]models.AlertRule, 0, len(alerts))
	for _, da := range alerts {
		al := l.New("ruleID", da.ID, "ruleName", da.Name)
		alertRule, err := om.migrateAlert(ctx, al, da, dash, migratedFolder)
		if err != nil {
			al.Warn("failed to migrate alert", "error", err)
			du.AddAlertErrors(err, da)
			continue
		}

		if dedupSet.contains(alertRule.Title) {
			dedupedName := dedupSet.deduplicate(alertRule.Title)
			al.Warn("duplicate alert rule name detected, renaming", "old_name", alertRule.Title, "new_name", dedupedName)
			alertRule.Title = dedupedName
		}
		dedupSet.add(alertRule.Title)
		om.silences = append(om.silences, createSilences(al, da, alertRule)...)
		rules = append(rules, *alertRule)

		pair := du.AddAlert(da)
		attachAlertRule(pair, alertRule)
	}

	if len(rules) > 0 {
		l.Info("Inserting migrated alert rules", "count", len(rules), "provisioned", provisioned)
		err = om.migrationStore.InsertAlertRules(ctx, om.orgID, rules, provisioned)
		if err != nil {
			du.MigratedAlerts = nil // Don't want duplicates.
			return du, fmt.Errorf("failed to insert alert rules: %w", err)
		}
	}

	return du, nil
}

func (ms *MigrationService) migrateOrg(ctx context.Context, orgID int64) (*orgMigration, error) {
	migrated, err := ms.migrationStore.IsMigrated(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("getting migration status: %w", err)
	}
	if migrated {
		return nil, ActiveMigrationError
	}

	om := ms.newOrgMigration(orgID)
	om.log.Info("migrating alerts for organisation")

	mappedAlerts, cnt, err := ms.migrationStore.GetOrgDashboardAlerts(ctx, orgID)
	if err != nil {
		om.summary.Errors = append(om.summary.Errors, err.Error())
	}
	om.log.Info("Alerts found to migrate", "alerts", cnt)

	for dashID, alerts := range mappedAlerts {
		du, err := om.migrateDashboard(ctx, dashID, alerts)
		if err != nil {
			l := om.log.New(
				"dashboardTitle", du.DashboardName,
				"dashboardUID", du.DashboardUID,
				"newFolderName", du.NewFolderName,
				"newFolderUID", du.NewFolderUID,
				"folderUID", du.FolderUID,
				"folderName", du.FolderName,
			)
			l.Warn("failed to migrate dashboard", "alertCount", len(alerts), "error", err)
			if du == nil {
				du = newDashboardUpgrade(dashID)
			}
			du.AddAlertErrors(err, alerts...)
		}
		om.summary.MigratedDashboards = append(om.summary.MigratedDashboards, du)
	}

	amConfig, err := om.setupAlertmanagerConfigs(ctx)
	if err != nil {
		om.summary.Errors = append(om.summary.Errors, fmt.Errorf("failed to setup AlertmanagerConfig: %w", err).Error())
	}

	// Validate the alertmanager configuration produced, this gives a chance to catch bad configuration at migration time.
	// Validation between legacy and unified alerting can be different (e.g. due to bug fixes) so this would fail the migration in that case.
	if err := om.validateAlertmanagerConfig(amConfig); err != nil {
		om.summary.Errors = append(om.summary.Errors, fmt.Errorf("failed to validate AlertmanagerConfig: %w", err).Error())
	}

	if err := om.writeSilencesFile(); err != nil {
		om.summary.Errors = append(om.summary.Errors, fmt.Errorf("failed to write silence file: %w", err).Error())
	}

	ms.log.Info("Writing alertmanager config", "orgID", orgID, "receivers", len(amConfig.AlertmanagerConfig.Receivers), "routes", len(amConfig.AlertmanagerConfig.Route.Routes))
	if err := ms.migrationStore.SaveAlertmanagerConfiguration(ctx, orgID, amConfig); err != nil {
		om.summary.Errors = append(om.summary.Errors, fmt.Errorf("failed to write AlertmanagerConfig: %w", err).Error())
	}

	om.summary.CreatedFolders = make([]string, 0, len(om.folderHelper.createdFolders))
	for _, f := range om.folderHelper.createdFolders {
		om.summary.CreatedFolders = append(om.summary.CreatedFolders, f.UID)
	}

	err = ms.migrationStore.SetOrgMigrationSummary(ctx, orgID, om.summary)
	if err != nil {
		return nil, err
	}
	err = ms.migrationStore.SetMigrated(ctx, orgID, true)
	if err != nil {
		return nil, fmt.Errorf("setting migration status: %w", err)
	}

	return om, nil
}

// migrateAllOrgs executes the migration for all orgs.
func (ms *MigrationService) migrateAllOrgs(ctx context.Context) error {
	orgs, err := ms.migrationStore.GetAllOrgs(ctx)
	if err != nil {
		return fmt.Errorf("can't get org list: %w", err)
	}

	for _, o := range orgs {
		_, err := ms.migrateOrg(ctx, o.ID)
		if err != nil {
			if errors.Is(err, ActiveMigrationError) {
				ms.log.Warn("skipping org, active migration already exists", "orgID", o.ID)
				continue
			}
			return fmt.Errorf("failed to migrate org %d: %w", o.ID, err)
		}
	}

	err = ms.migrationStore.SetMigrated(ctx, anyOrg, true)
	if err != nil {
		return fmt.Errorf("setting migration status: %w", err)
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

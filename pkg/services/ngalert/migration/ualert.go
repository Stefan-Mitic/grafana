package migration

import (
	"context"
	"errors"
	"fmt"

	legacymodels "github.com/grafana/grafana/pkg/services/alerting/models"
	migmodels "github.com/grafana/grafana/pkg/services/ngalert/migration/models"
	"github.com/grafana/grafana/pkg/services/ngalert/models"
	"github.com/grafana/grafana/pkg/services/ngalert/store"
)

func (om *OrgMigration) migrateAndSaveAlerts(ctx context.Context, alerts []*legacymodels.Alert, info migmodels.DashboardUpgradeInfo) ([]*migmodels.AlertPair, error) {
	log := om.log.New(
		"dashboardTitle", info.DashboardName,
		"dashboardUID", info.DashboardUID,
		"newFolderName", info.NewFolderName,
		"newFolderUID", info.NewFolderUID,
		"folderUID", info.FolderUID,
		"folderName", info.FolderName,
	)

	// Here we ensure that the alert rule title is unique within the folder.
	if _, ok := om.alertRuleTitleDedup[info.NewFolderUID]; !ok {
		om.alertRuleTitleDedup[info.NewFolderUID] = Deduplicator{
			set:             make(map[string]struct{}),
			caseInsensitive: om.dialect.SupportEngine(),
			maxLen:          store.AlertDefinitionMaxTitleLength,
		}
	}
	dedupSet := om.alertRuleTitleDedup[info.NewFolderUID]

	alertCnt := 0
	rules := make([]models.AlertRule, 0, len(alerts))
	pairs := make([]*migmodels.AlertPair, 0, len(alerts))
	for _, da := range alerts {
		al := log.New("ruleID", da.ID, "ruleName", da.Name)

		alertRule, err := om.migrateAlert(ctx, al, da, info)
		if err != nil {
			al.Warn("failed to migrate alert", "error", err)
			pair := newAlertPair(da)
			pair.Error = err.Error()
			pairs = append(pairs, pair)
			continue
		}

		if dedupSet.contains(alertRule.Title) {
			dedupedName := dedupSet.deduplicate(alertRule.Title)
			al.Warn("duplicate alert rule name detected, renaming", "old_name", alertRule.Title, "new_name", dedupedName)
			alertRule.Title = dedupedName
		}
		dedupSet.add(alertRule.Title)
		om.silences = append(om.silences, createSilences(al, da, alertRule)...)

		pair := newAlertPair(da)
		pair.AttachAlertRule(*alertRule)
		pairs = append(pairs, pair)
		rules = append(rules, *alertRule)
	}

	if len(rules) > 0 {
		log.Info("Inserting migrated alert rules", "count", alertCnt, "provisioned", info.Provisioned)
		for _, pair := range pairs {
			if pair.AlertRule != nil {
				// We insert one by one so that we can catch duplicate constraint errors on the alert rule title and fix them.
				err := om.migrationStore.InsertAlertRules(ctx, pair.GetAlertRule())
				if err != nil {
					if errors.Is(err, models.ErrAlertRuleUniqueConstraintViolation) {
						// Retry with a deduplicated title.
						rule := pair.GetAlertRule()
						dedupedName := dedupSet.deduplicate(rule.Title)
						log.Warn("duplicate alert rule name detected, renaming", "old_name", rule.Title, "new_name", dedupedName)
						rule.Title = dedupedName
						pair.AttachAlertRule(rule)
						err := om.migrationStore.InsertAlertRules(ctx, rule)
						if err == nil {
							continue
						}
					}
					return nil, fmt.Errorf("failed to insert alert rules: %w", err)
				}
			}
		}

		if info.Provisioned {
			err := om.migrationStore.UpsertProvenance(ctx, om.orgID, models.ProvenanceUpgrade, rules)
			if err != nil {
				return nil, err
			}
		}
	}

	return pairs, nil
}

func (om *OrgMigration) migrateAndSaveDashboard(ctx context.Context, dashID int64, alerts []*legacymodels.Alert, skipExisting bool) (*migmodels.DashboardUpgrade, migmodels.OrgMigrationSummary, error) {
	summary := migmodels.OrgMigrationSummary{}
	du := om.state.PopDashboardUpgrade(dashID)
	if du != nil {
		if skipExisting {
			alerts = du.ExcludeExisting(alerts...)
		} else {
			err := om.cleanupDashboardAlerts(ctx, du)
			if err != nil {
				return nil, summary, fmt.Errorf("cleanup: %w", err)
			}
			du = nil
		}
	}

	// If we aren't replacing the dashboard, we try to reuse the existing migrated folder so that new alerts rules are saved to the same folder as the existing ones.
	if du == nil {
		var err error
		du, err = om.folderHelper.createMigratedDashboardUpgrade(ctx, om.log, dashID)
		if err != nil {
			// We don't return error here because we want to display the error in the summary and continue with other dashboards.
			om.log.Warn("Failed to migrate dashboard", "alertCount", len(alerts), "error", err)
			pairs := du.AddAlertErrors(err, alerts...)
			summary.CountDashboardAlerts(pairs...)
			return du, summary, nil
		}
	}

	pairs, err := om.migrateAndSaveAlerts(ctx, alerts, *du.DashboardUpgradeInfo)
	if err != nil {
		om.log.Warn("Failed to migrate dashboard alerts", "alertCount", len(alerts), "error", err)
		pairs := du.AddAlertErrors(err, alerts...)
		summary.CountDashboardAlerts(pairs...)
		return du, summary, nil
	}

	du.MigratedAlerts = append(du.MigratedAlerts, pairs...)
	summary.CountDashboardAlerts(pairs...)
	return du, summary, nil
}

func (om *OrgMigration) migrateOrg(ctx context.Context, skipExisting bool) (migmodels.OrgMigrationSummary, error) {
	om.log.Info("migrating alerts for organisation")

	summary, err := om.migrateOrgAlerts(ctx, skipExisting)
	if err != nil {
		om.state.AddError(err.Error())
	}

	channelSummary, err := om.migrateOrgChannels(ctx, skipExisting)
	if err != nil {
		om.state.AddError(err.Error())
	}

	summary.Add(channelSummary)
	return summary, nil
}

func (om *OrgMigration) migrateOrgAlerts(ctx context.Context, skipExisting bool) (migmodels.OrgMigrationSummary, error) {
	summary := migmodels.OrgMigrationSummary{}
	mappedAlerts, cnt, err := om.migrationStore.GetOrgDashboardAlerts(ctx, om.orgID)
	if err != nil {
		return summary, fmt.Errorf("failed to load alerts: %w", err)
	}
	om.log.Info("Alerts found to migrate", "alerts", cnt)

	for dashID, alerts := range mappedAlerts {
		du, sum, err := om.migrateAndSaveDashboard(ctx, dashID, alerts, skipExisting)
		if err != nil {
			return summary, err
		}

		om.state.MigratedDashboards = append(om.state.MigratedDashboards, du)
		summary.Add(sum)
	}
	return summary, nil
}

func (om *OrgMigration) migrateOrgChannels(ctx context.Context, skipExisting bool) (migmodels.OrgMigrationSummary, error) {
	summary := migmodels.OrgMigrationSummary{}
	channels, err := om.migrationStore.GetNotificationChannels(ctx, om.orgID)
	if err != nil {
		return summary, fmt.Errorf("load notification channels: %w", err)
	}
	pairs, err := om.migrateAndSaveChannels(ctx, channels, skipExisting)
	if err != nil {
		return summary, err
	}
	summary.CountChannels(pairs...)
	return summary, nil
}

func (om *OrgMigration) migrateAndSaveChannels(ctx context.Context, channels []*legacymodels.AlertNotification, skipExisting bool) ([]*migmodels.ContactPair, error) {
	cfg, err := om.migrationStore.GetAlertmanagerConfig(ctx, om.orgID)
	if err != nil {
		return nil, fmt.Errorf("failed to get alertmanager config: %w", err)
	}

	amConfig := FromPostableUserConfig(cfg)
	if skipExisting {
		channels = om.state.ExcludeExisting(channels...)
	} else {
		amConfig.cleanupReceiversAndRoutes(om.state.PopContactPairs(channels...)...)
	}

	pairs, err := om.migrateChannels(amConfig, channels)
	if err != nil {
		return nil, fmt.Errorf("migrateChannels: %w", err)
	}
	om.state.MigratedChannels = append(om.state.MigratedChannels, pairs...)

	// Validate the alertmanager configuration produced, this gives a chance to catch bad configuration at migration time.
	// Validation between legacy and unified alerting can be different (e.g. due to bug fixes) so this would fail the migration in that case.
	if err := om.validateAlertmanagerConfig(amConfig.PostableUserConfig); err != nil {
		return nil, fmt.Errorf("failed to validate AlertmanagerConfig: %w", err)
	}

	om.log.Info("Writing alertmanager config", "orgID", om.orgID, "receivers", len(amConfig.AlertmanagerConfig.Receivers), "routes", len(amConfig.AlertmanagerConfig.Route.Routes))
	if err := om.migrationStore.SaveAlertmanagerConfiguration(ctx, om.orgID, amConfig.PostableUserConfig); err != nil {
		return nil, fmt.Errorf("failed to write AlertmanagerConfig: %w", err)
	}

	return pairs, nil
}

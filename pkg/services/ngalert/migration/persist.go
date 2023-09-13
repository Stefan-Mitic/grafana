package migration

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/grafana/grafana/pkg/infra/kvstore"
	"github.com/grafana/grafana/pkg/infra/log"
	apimodels "github.com/grafana/grafana/pkg/services/ngalert/api/tooling/definitions"
	"github.com/grafana/grafana/pkg/services/ngalert/models"
)

// KVNamespace is the kvstore namespace used for the migration status.
const KVNamespace = "ngalert.migration"

// migratedKey is the kvstore key used for the migration status.
const migratedKey = "migrated"

// createdFoldersKey is the kvstore key used for the list of created folder UIDs.
const createdFoldersKey = "createdFolders"

// RuleStore represents the ability to persist and query alert rules.
type RuleStore interface {
	InsertAlertRules(ctx context.Context, rule []models.AlertRule) (map[string]int64, error)
}

// AlertingStore is the database interface used by the Alertmanager service.
type AlertingStore interface {
	SaveAlertmanagerConfiguration(ctx context.Context, cmd *models.SaveAlertmanagerConfigurationCmd) error
}

// insertRules inserts the given rules into the database.
func (ms *MigrationService) insertRules(ctx context.Context, l log.Logger, orgRules map[*models.AlertRule][]uidOrID) error {
	l.Info("Inserting migrated alert rules", "count", len(orgRules))
	rules := make([]models.AlertRule, 0, len(orgRules))
	for rule := range orgRules {
		rules = append(rules, *rule)
	}
	_, err := ms.ruleStore.InsertAlertRules(ctx, rules)
	if err != nil {
		return err
	}
	return nil
}

// writeAlertmanagerConfig writes the given Alertmanager configuration to the database.
func (ms *MigrationService) writeAlertmanagerConfig(ctx context.Context, orgID int64, amConfig *apimodels.PostableUserConfig) error {
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

type InfoStore struct {
	kv *kvstore.NamespacedKVStore
}

// IsMigrated returns the migration status from the kvstore.
func (ms *InfoStore) IsMigrated(ctx context.Context) (bool, error) {
	content, exists, err := ms.kv.Get(ctx, migratedKey)
	if err != nil {
		return false, err
	}

	if !exists {
		return false, nil
	}

	return strconv.ParseBool(content)
}

// setMigrated sets the migration status in the kvstore.
func (ms *InfoStore) setMigrated(ctx context.Context, migrated bool) error {
	return ms.kv.Set(ctx, migratedKey, strconv.FormatBool(migrated))
}

// GetCreatedFolders returns a map of orgID to list of folder UIDs that were created by the migration from the kvstore.
func (ms *InfoStore) GetCreatedFolders(ctx context.Context) (map[int64][]string, error) {
	content, exists, err := ms.kv.Get(ctx, createdFoldersKey)
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

// setCreatedFolders sets the map of orgID to list of folder UIDs that were created by the migration in the kvstore.
func (ms *InfoStore) setCreatedFolders(ctx context.Context, folderUids map[int64][]string) error {
	raw, err := json.Marshal(folderUids)
	if err != nil {
		return err
	}

	return ms.kv.Set(ctx, createdFoldersKey, string(raw))
}

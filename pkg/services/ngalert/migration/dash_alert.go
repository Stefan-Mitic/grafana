package migration

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/grafana/grafana/pkg/infra/db"
	"github.com/grafana/grafana/pkg/infra/log"
	legacymodels "github.com/grafana/grafana/pkg/services/alerting/models"
)

// slurpDashAlerts loads all legacy dashboard alerts for the given org mapped by dashboard id.
func (m *migration) slurpDashAlerts(ctx context.Context, l log.Logger, orgID int64) (map[int64][]*legacymodels.Alert, error) {
	var dashAlerts []*legacymodels.Alert
	err := m.store.WithDbSession(ctx, func(sess *db.Session) error {
		return sess.SQL("select * from alert WHERE org_id = ?", orgID).Find(&dashAlerts)
	})
	if err != nil {
		return nil, fmt.Errorf("could not load alerts: %w", err)
	}

	l.Info("Alerts found to migrate", "alerts", len(dashAlerts))
	mappedAlerts := make(map[int64][]*legacymodels.Alert)
	for _, alert := range dashAlerts {
		mappedAlerts[alert.DashboardID] = append(mappedAlerts[alert.DashboardID], alert)
	}

	return mappedAlerts, nil
}

// dashAlertSettings is a type for the JSON that is in the settings field of
// the alert table.
type dashAlertSettings struct {
	NoDataState         string               `json:"noDataState"`
	ExecutionErrorState string               `json:"executionErrorState"`
	Conditions          []dashAlertCondition `json:"conditions"`
	AlertRuleTags       any                  `json:"alertRuleTags"`
	Notifications       []dashAlertNot       `json:"notifications"`
}

// dashAlertNot is the object that represents the Notifications array in
// dashAlertSettings
type dashAlertNot struct {
	UID string `json:"uid,omitempty"`
	ID  int64  `json:"id,omitempty"`
}

// dashAlertingConditionJSON is like classic.ClassicConditionJSON except that it
// includes the model property with the query.
type dashAlertCondition struct {
	Evaluator conditionEvalJSON `json:"evaluator"`

	Operator struct {
		Type string `json:"type"`
	} `json:"operator"`

	Query struct {
		Params       []string `json:"params"`
		DatasourceID int64    `json:"datasourceId"`
		Model        json.RawMessage
	} `json:"query"`

	Reducer struct {
		// Params []any `json:"params"` (Unused)
		Type string `json:"type"`
	}
}

type conditionEvalJSON struct {
	Params []float64 `json:"params"`
	Type   string    `json:"type"` // e.g. "gt"
}

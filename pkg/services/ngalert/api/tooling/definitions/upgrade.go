package definitions

import (
	"github.com/prometheus/common/model"

	legacymodels "github.com/grafana/grafana/pkg/services/alerting/models"
)

// swagger:route GET /api/v1/upgrade upgrade RouteGetOrgUpgrade
//
// Get existing alerting upgrade for current organization.
//
//     Produces:
//     - application/json
//
//     Responses:
//       200: OrgMigrationSummary

// swagger:route POST /api/v1/upgrade upgrade RoutePostUpgradeOrg
//
// Upgrade alerting for current organization.
//
//     Produces:
//     - application/json
//
//     Responses:
//       200: OrgMigrationSummary

// swagger:route DELETE /api/v1/upgrade upgrade RouteDeleteOrgUpgrade
//
// Delete existing alerting upgrade for current organization.
//
//     Produces:
//     - application/json
//
//     Responses:
//       200: Ack

// swagger:model
type OrgMigrationSummary struct {
	OrgID              int64               `json:"orgId"`
	MigratedDashboards []*DashboardUpgrade `json:"migratedDashboards"`
	MigratedChannels   []*ContactPair      `json:"migratedChannels"`
	CreatedFolders     []string            `json:"createdFolders"`
	Errors             []string            `json:"errors"`
}

type DashboardUpgrade struct {
	MigratedAlerts []*AlertPair `json:"migratedAlerts"`
	DashboardID    int64        `json:"dashboardId"`
	DashboardUID   string       `json:"dashboardUid"`
	DashboardName  string       `json:"dashboardName"`
	FolderUID      string       `json:"folderUid"`
	FolderName     string       `json:"folderName"`
	NewFolderUID   string       `json:"newFolderUid,omitempty"`
	NewFolderName  string       `json:"newFolderName,omitempty"`
	Provisioned    bool         `json:"provisioned"`
	Errors         []string     `json:"errors"`
}

func (du *DashboardUpgrade) SetErrors(alerts []*legacymodels.Alert, err error) {
	du.Errors = append(du.Errors, err.Error())
	du.AddAlertErrors(err, alerts...)
}

func (du *DashboardUpgrade) SetDashboard(uid, name string) {
	du.DashboardUID = uid
	du.DashboardName = name
}
func (du *DashboardUpgrade) SetFolder(uid, name string) {
	du.FolderUID = uid
	du.FolderName = name
}
func (du *DashboardUpgrade) SetNewFolder(uid, name string) {
	du.NewFolderUID = uid
	du.NewFolderName = name
}
func (du *DashboardUpgrade) AddAlert(da *legacymodels.Alert) *AlertPair {
	pair := &AlertPair{
		LegacyAlert: &LegacyAlert{
			Modified:       false,
			ID:             da.ID,
			DashboardID:    da.DashboardID,
			PanelID:        da.PanelID,
			Name:           da.Name,
			Paused:         da.State == legacymodels.AlertStatePaused,
			Silenced:       da.Silenced,
			ExecutionError: da.ExecutionError,
			Frequency:      da.Frequency,
			For:            model.Duration(da.For),
		},
	}
	du.MigratedAlerts = append(du.MigratedAlerts, pair)
	return pair
}
func (du *DashboardUpgrade) AddAlertErrors(err error, alerts ...*legacymodels.Alert) {
	for _, da := range alerts {
		pair := du.AddAlert(da)
		pair.Error = err.Error()
	}
}

type AlertPair struct {
	LegacyAlert *LegacyAlert      `json:"legacyAlert"`
	AlertRule   *AlertRuleUpgrade `json:"alertRule"`
	Error       string            `json:"error,omitempty"`
}

type ContactPair struct {
	LegacyChannel       *LegacyChannel       `json:"legacyChannel"`
	ContactPointUpgrade *ContactPointUpgrade `json:"contactPoint"`
	Provisioned         bool                 `json:"provisioned"`
	Error               string               `json:"error,omitempty"`
}

type LegacyAlert struct {
	ID             int64          `json:"id"`
	DashboardID    int64          `json:"dashboardId"`
	PanelID        int64          `json:"panelId"`
	Name           string         `json:"name"`
	Paused         bool           `json:"paused"`
	Silenced       bool           `json:"silenced"`
	ExecutionError string         `json:"executionError"`
	Frequency      int64          `json:"frequency"`
	For            model.Duration `json:"for"`

	Modified bool `json:"modified"`
}

type AlertRuleUpgrade struct {
	UID          string              `json:"uid"`
	Title        string              `json:"title"`
	DashboardUID *string             `json:"dashboardUid"`
	PanelID      *int64              `json:"panelId"`
	NoDataState  NoDataState         `json:"noDataState"`
	ExecErrState ExecutionErrorState `json:"execErrState"`
	For          model.Duration      `json:"for"`
	Annotations  map[string]string   `json:"annotations"`
	Labels       map[string]string   `json:"labels"`
	IsPaused     bool                `json:"isPaused"`

	Modified bool `json:"modified"`
}

type LegacyChannel struct {
	ID                    int64          `json:"id"`
	UID                   string         `json:"uid"`
	Name                  string         `json:"name"`
	Type                  string         `json:"type"`
	SendReminder          bool           `json:"sendReminder"`
	DisableResolveMessage bool           `json:"disableResolveMessage"`
	Frequency             model.Duration `json:"frequency"`
	IsDefault             bool           `json:"isDefault"`
	Modified              bool           `json:"modified"`
}

type ContactPointUpgrade struct {
	Name                  string `json:"name"`
	UID                   string `json:"uid"`
	Type                  string `json:"type"`
	DisableResolveMessage bool   `json:"disableResolveMessage"`
	Modified              bool   `json:"modified"`
}

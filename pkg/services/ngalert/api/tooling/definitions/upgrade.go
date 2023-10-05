package definitions

import (
	"github.com/prometheus/common/model"

	legacymodels "github.com/grafana/grafana/pkg/services/alerting/models"
)

// swagger:route GET /api/v1/upgrade/org upgrade RouteGetOrgUpgrade
//
// Get existing alerting upgrade for current organization.
//
//     Produces:
//     - application/json
//
//     Responses:
//       200: OrgMigrationSummary

// swagger:route POST /api/v1/upgrade/org upgrade RoutePostUpgradeOrg
//
// Upgrade all legacy alerts for current organization.
//
//     Produces:
//     - application/json
//
//     Responses:
//       200: OrgMigrationSummary

// swagger:route DELETE /api/v1/upgrade/org upgrade RouteDeleteOrgUpgrade
//
// Delete existing alerting upgrade for current organization.
//
//     Produces:
//     - application/json
//
//     Responses:
//       200: Ack

// swagger:route POST /api/v1/upgrade/dashboard/{DashboardID}/panel/{PanelID} upgrade RoutePostUpgradeAlert
//
// Upgrade single legacy dashboard alert.
//
//     Produces:
//     - application/json
//
//     Responses:
//       200: DashboardUpgrade

// swagger:route POST /api/v1/upgrade/dashboard/{DashboardID} upgrade RoutePostUpgradeDashboard
//
// Upgrade all legacy dashboard alerts on a dashboard.
//
//     Produces:
//     - application/json
//
//     Responses:
//       200: DashboardUpgrade

// swagger:parameters RoutePostUpgradeAlert RoutePostUpgradeDashboard
type DashboardParam struct {
	// Dashboard ID of dashboard alert.
	// in:path
	// required:true
	DashboardID string
}

// swagger:parameters RoutePostUpgradeAlert
type PanelParam struct {
	// Panel ID of dashboard alert.
	// in:path
	// required:true
	PanelID string
}

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
	Warnings       []string     `json:"warnings"`
}

func (oms *OrgMigrationSummary) GetDashboardUpgrade(dashboardId int64) *DashboardUpgrade {
	for _, du := range oms.MigratedDashboards {
		if du.DashboardID == dashboardId {
			return du
		}
	}
	return nil
}

func (oms *OrgMigrationSummary) PopDashboardUpgrade(dashboardId int64) *DashboardUpgrade {
	for i, du := range oms.MigratedDashboards {
		if du.DashboardID == dashboardId {
			oms.MigratedDashboards = append(oms.MigratedDashboards[:i], oms.MigratedDashboards[i+1:]...)
			return du
		}
	}
	return nil
}

func (du *DashboardUpgrade) PopAlertPairByPanelID(panelID int64) *AlertPair {
	for i, pair := range du.MigratedAlerts {
		if pair.LegacyAlert.PanelID == panelID {
			du.MigratedAlerts = append(du.MigratedAlerts[:i], du.MigratedAlerts[i+1:]...)
			return pair
		}
	}
	return nil
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

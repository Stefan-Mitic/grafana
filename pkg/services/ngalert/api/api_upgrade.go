package api

import (
	"context"
	"net/http"

	"github.com/grafana/grafana/pkg/api/response"
	"github.com/grafana/grafana/pkg/infra/log"
	contextmodel "github.com/grafana/grafana/pkg/services/contexthandler/model"
	apiModels "github.com/grafana/grafana/pkg/services/ngalert/api/tooling/definitions"
	"github.com/grafana/grafana/pkg/setting"
	"github.com/grafana/grafana/pkg/util"
)

type UpgradeService interface {
	MigrateOrg(ctx context.Context, orgID int64) (*apiModels.OrgMigrationSummary, error)
	GetOrgMigrationSummary(ctx context.Context, orgID int64) (*apiModels.OrgMigrationSummary, error)
	RevertOrg(ctx context.Context, orgID int64) error
}

type UpgradeSrv struct {
	log            log.Logger
	upgradeService UpgradeService
	cfg            *setting.Cfg
}

func NewUpgradeSrc(
	log log.Logger,
	upgradeService UpgradeService,
	cfg *setting.Cfg,
) *UpgradeSrv {
	return &UpgradeSrv{
		log:            log,
		upgradeService: upgradeService,
		cfg:            cfg,
	}
}

func (srv *UpgradeSrv) RoutePostUpgradeOrg(c *contextmodel.ReqContext) response.Response {
	// If UA is enabled, we don't want to allow the user to use this endpoint to upgrade anymore.
	if srv.cfg.UnifiedAlerting.IsEnabled() {
		return response.Error(http.StatusForbidden, "This endpoint is not available with UA enabled.", nil)
	}

	summary, err := srv.upgradeService.MigrateOrg(c.Req.Context(), c.OrgID)
	if err != nil {
		return response.Error(http.StatusInternalServerError, "Server error", err)
	}
	//return response.JSON(http.StatusOK, util.DynMap{"message": "Grafana Alerting resources created based on existing alerts and notification channels."})
	return response.JSON(http.StatusOK, summary)
}

func (srv *UpgradeSrv) RouteGetOrgUpgrade(c *contextmodel.ReqContext) response.Response {
	// If UA is enabled, we don't want to allow the user to use this endpoint to upgrade anymore.
	if srv.cfg.UnifiedAlerting.IsEnabled() {
		return response.Error(http.StatusForbidden, "This endpoint is not available with UA enabled.", nil)
	}

	summary, err := srv.upgradeService.GetOrgMigrationSummary(c.Req.Context(), c.OrgID)
	if err != nil {
		return response.Error(http.StatusInternalServerError, "Server error", err)
	}
	return response.JSON(http.StatusOK, summary)
}

func (srv *UpgradeSrv) RouteDeleteOrgUpgrade(c *contextmodel.ReqContext) response.Response {
	// If UA is enabled, we don't want to allow the user to use this endpoint to upgrade anymore.
	if srv.cfg.UnifiedAlerting.IsEnabled() {
		return response.Error(http.StatusForbidden, "This endpoint is not available with UA enabled.", nil)
	}

	err := srv.upgradeService.RevertOrg(c.Req.Context(), c.OrgID)
	if err != nil {
		return response.Error(http.StatusInternalServerError, "Server error", err)
	}
	return response.JSON(http.StatusOK, util.DynMap{"message": "Grafana Alerting resources deleted for this organization."})
}

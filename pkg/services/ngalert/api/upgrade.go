package api

import (
	"github.com/grafana/grafana/pkg/api/response"
	contextmodel "github.com/grafana/grafana/pkg/services/contexthandler/model"
)

type UpgradeApiHandler struct {
	svc *UpgradeSrv
}

func NewUpgradeApi(svc *UpgradeSrv) *UpgradeApiHandler {
	return &UpgradeApiHandler{
		svc: svc,
	}
}

func (f *UpgradeApiHandler) handleRoutePostUpgradeOrg(ctx *contextmodel.ReqContext) response.Response {
	return f.svc.RoutePostUpgradeOrg(ctx)
}

func (f *UpgradeApiHandler) handleRouteGetOrgUpgrade(ctx *contextmodel.ReqContext) response.Response {
	return f.svc.RouteGetOrgUpgrade(ctx)
}

func (f *UpgradeApiHandler) handleRouteDeleteOrgUpgrade(ctx *contextmodel.ReqContext) response.Response {
	return f.svc.RouteDeleteOrgUpgrade(ctx)
}

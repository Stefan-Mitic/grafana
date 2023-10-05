package migration

import (
	"strings"

	pb "github.com/prometheus/alertmanager/silence/silencepb"
	"github.com/prometheus/common/model"

	"github.com/grafana/grafana/pkg/infra/log"
	legacymodels "github.com/grafana/grafana/pkg/services/alerting/models"
	apiModels "github.com/grafana/grafana/pkg/services/ngalert/api/tooling/definitions"
	migmodels "github.com/grafana/grafana/pkg/services/ngalert/migration/models"
	migrationStore "github.com/grafana/grafana/pkg/services/ngalert/migration/store"
	"github.com/grafana/grafana/pkg/services/secrets"
	"github.com/grafana/grafana/pkg/services/sqlstore/migrator"
	"github.com/grafana/grafana/pkg/util"
)

// OrgMigration is a helper struct for migrating alerts for a single org. It contains state, services, and caches.
type OrgMigration struct {
	orgID int64
	log   log.Logger

	dialect           migrator.Dialect
	migrationStore    migrationStore.Store
	encryptionService secrets.Service

	dataPath string

	folderHelper folderHelper

	seenUIDs            Deduplicator
	silences            []*pb.MeshSilence
	alertRuleTitleDedup map[string]Deduplicator // Folder -> Deduplicator (Title).

	state *migmodels.OrgMigrationState
}

func newContactPair(channel *legacymodels.AlertNotification, contactPoint *apiModels.PostableApiReceiver, route *apiModels.Route, err error) *migmodels.ContactPair {
	pair := &migmodels.ContactPair{
		LegacyChannel: &migmodels.LegacyChannel{
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
		Provisioned: false, //TODO: implement
	}
	if contactPoint != nil {
		pair.ContactPointUpgrade = &migmodels.ContactPointUpgrade{
			Modified:              false,
			Name:                  contactPoint.Name,
			UID:                   contactPoint.GrafanaManagedReceivers[0].UID,
			Type:                  contactPoint.GrafanaManagedReceivers[0].Type,
			DisableResolveMessage: contactPoint.GrafanaManagedReceivers[0].DisableResolveMessage,
		}
		if route != nil {
			pair.ContactPointUpgrade.RouteLabel = route.ObjectMatchers[0].Name
		}
	}

	if err != nil {
		pair.Error = err.Error()
	}
	return pair
}

type MigratedAlertmanagerConfig struct {
	*apiModels.PostableUserConfig
	legacyRoute *apiModels.Route
}

func (mac *MigratedAlertmanagerConfig) cleanupReceiversAndRoutes(pairs ...*migmodels.ContactPair) {
	// Find all previously migrated ContactPairs for these channels.
	upgradesToReplace := make(map[string]*migmodels.ContactPointUpgrade)
	for _, pair := range pairs {
		if pair.ContactPointUpgrade != nil && pair.ContactPointUpgrade.Name != "" {
			upgradesToReplace[pair.ContactPointUpgrade.Name] = pair.ContactPointUpgrade
		}
	}

	// Remove receivers for channels that are being replaced.
	var keptReceivers []*apiModels.PostableApiReceiver
	for _, recv := range mac.AlertmanagerConfig.Receivers {
		if _, ok := upgradesToReplace[recv.Name]; !ok {
			keptReceivers = append(keptReceivers, recv)
		} else {
			// Don't keep receiver and remove all nested routes that reference it.
			// This will fail validation if the user has created other routes that reference this receiver.
			// In that case, they must manually delete the added routes.
			mac.removeRoutesForReceiver(recv.Name)
		}
	}
	mac.AlertmanagerConfig.Receivers = keptReceivers
}

func (mac *MigratedAlertmanagerConfig) removeRoutesForReceiver(recv string) {
	var keptRoutes []*apiModels.Route
	for i, route := range mac.legacyRoute.Routes {
		if route.Receiver != recv {
			keptRoutes = append(keptRoutes, mac.legacyRoute.Routes[i])
		}
	}
	mac.legacyRoute.Routes = keptRoutes
}

func FromPostableUserConfig(config *apiModels.PostableUserConfig) *MigratedAlertmanagerConfig {
	if config == nil {
		// No existing amConfig created from a previous migration.
		c, r := createBaseConfig()
		return &MigratedAlertmanagerConfig{
			PostableUserConfig: c,
			legacyRoute:        r,
		}
	} else if config.AlertmanagerConfig.Route == nil {
		// No existing base route created from a previous migration.
		defaultRoute, nestedLegacyChannelRoute := createDefaultRoute()
		config.AlertmanagerConfig.Route = defaultRoute
		return &MigratedAlertmanagerConfig{
			PostableUserConfig: config,
			legacyRoute:        nestedLegacyChannelRoute,
		}
	}
	return &MigratedAlertmanagerConfig{
		PostableUserConfig: config,
		legacyRoute:        getOrCreateNestedLegacyRoute(config),
	}
}

// Deduplicator is a wrapper around map[string]struct{} and util.GenerateShortUID() which aims help maintain and generate
// unique strings (such as uids or titles). if caseInsensitive is true, all uniqueness is determined in a
// case-insensitive manner. if maxLen is greater than 0, all strings will be truncated to maxLen before being checked in
// contains and dedup will always return a string of length maxLen or less.
type Deduplicator struct {
	set             map[string]struct{}
	caseInsensitive bool
	maxLen          int
}

// contains checks whether the given string has already been seen by this Deduplicator.
func (s *Deduplicator) contains(u string) bool {
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
func (s *Deduplicator) deduplicate(dedup string) string {
	uid := util.GenerateShortUID()
	if s.maxLen > 0 && len(dedup)+1+len(uid) > s.maxLen {
		trunc := s.maxLen - 1 - len(uid)
		dedup = dedup[:trunc]
	}

	return dedup + "_" + uid
}

// add adds the given string to the Deduplicator.
func (s *Deduplicator) add(uid string) {
	dedup := uid
	if s.caseInsensitive {
		dedup = strings.ToLower(dedup)
	}
	s.set[dedup] = struct{}{}
}

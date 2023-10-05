package migration

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	alertingNotify "github.com/grafana/alerting/notify"
	"github.com/prometheus/alertmanager/config"
	"github.com/prometheus/alertmanager/pkg/labels"
	"github.com/prometheus/common/model"

	"github.com/grafana/grafana/pkg/components/simplejson"
	legacymodels "github.com/grafana/grafana/pkg/services/alerting/models"
	apimodels "github.com/grafana/grafana/pkg/services/ngalert/api/tooling/definitions"
	migmodels "github.com/grafana/grafana/pkg/services/ngalert/migration/models"
	ngmodels "github.com/grafana/grafana/pkg/services/ngalert/models"
	"github.com/grafana/grafana/pkg/services/secrets"
)

const (
	// DisabledRepeatInterval is a large duration that will be used as a pseudo-disable in case a legacy channel doesn't have SendReminders enabled.
	DisabledRepeatInterval = model.Duration(time.Duration(8736) * time.Hour) // 1y
)

// migrateChannels creates Alertmanager configs with migrated receivers and routes.
func (om *OrgMigration) migrateChannels(amConfig *MigratedAlertmanagerConfig, channels []*legacymodels.AlertNotification) ([]*migmodels.ContactPair, error) {
	// Create all newly migrated receivers from legacy notification channels.
	pairs := make([]*migmodels.ContactPair, 0, len(channels))
	for _, c := range channels {
		receiver, err := om.createReceiver(c)
		if err != nil {
			om.log.Warn("Failed to create receiver", "type", c.Type, "name", c.Name, "uid", c.UID, "error", err)
			pairs = append(pairs, newContactPair(c, receiver, nil, fmt.Errorf("create receiver: %w", err)))
			continue
		}

		route, err := createRoute(c, receiver.Name)
		if err != nil {
			om.log.Warn("Failed to create route for receiver", "type", c.Type, "name", c.Name, "uid", c.UID, "error", err)
			pairs = append(pairs, newContactPair(c, receiver, nil, fmt.Errorf("create route: %w", err)))
			continue
		}

		amConfig.legacyRoute.Routes = append(amConfig.legacyRoute.Routes, route)
		amConfig.AlertmanagerConfig.Receivers = append(amConfig.AlertmanagerConfig.Receivers, receiver)
		pairs = append(pairs, newContactPair(c, receiver, route, nil))
	}

	return pairs, nil
}

func getOrCreateNestedLegacyRoute(config *apimodels.PostableUserConfig) *apimodels.Route {
	for _, r := range config.AlertmanagerConfig.Route.Routes {
		if isNestedLegacyRoute(r) {
			return r
		}
	}
	nestedLegacyChannelRoute := createNestedLegacyRoute()
	// Add new nested route as the first of the top-level routes.
	config.AlertmanagerConfig.Route.Routes = append([]*apimodels.Route{nestedLegacyChannelRoute}, config.AlertmanagerConfig.Route.Routes...)
	return nestedLegacyChannelRoute
}

func isNestedLegacyRoute(r *apimodels.Route) bool {
	return len(r.ObjectMatchers) == 1 && r.ObjectMatchers[0].Name == UseLegacyChannelsLabel
}

// validateAlertmanagerConfig validates the alertmanager configuration produced by the migration against the receivers.
func (om *OrgMigration) validateAlertmanagerConfig(config *apimodels.PostableUserConfig) error {
	for _, r := range config.AlertmanagerConfig.Receivers {
		for _, gr := range r.GrafanaManagedReceivers {
			data, err := gr.Settings.MarshalJSON()
			if err != nil {
				return err
			}
			var (
				cfg = &alertingNotify.GrafanaIntegrationConfig{
					UID:                   gr.UID,
					Name:                  gr.Name,
					Type:                  gr.Type,
					DisableResolveMessage: gr.DisableResolveMessage,
					Settings:              data,
					SecureSettings:        gr.SecureSettings,
				}
			)

			_, err = alertingNotify.BuildReceiverConfiguration(context.Background(), &alertingNotify.APIReceiver{
				GrafanaIntegrations: alertingNotify.GrafanaIntegrations{Integrations: []*alertingNotify.GrafanaIntegrationConfig{cfg}},
			}, om.encryptionService.GetDecryptedValue)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// createNotifier creates a PostableGrafanaReceiver from a legacy notification channel.
func (om *OrgMigration) createNotifier(c *legacymodels.AlertNotification) (*apimodels.PostableGrafanaReceiver, error) {
	settings, secureSettings, err := om.migrateSettingsToSecureSettings(c.Type, c.Settings, c.SecureSettings)
	if err != nil {
		return nil, err
	}

	data, err := settings.MarshalJSON()
	if err != nil {
		return nil, err
	}

	return &apimodels.PostableGrafanaReceiver{
		UID:                   c.UID,
		Name:                  c.Name,
		Type:                  c.Type,
		DisableResolveMessage: c.DisableResolveMessage,
		Settings:              data,
		SecureSettings:        secureSettings,
	}, nil
}

// createReceiver creates a receiver from a legacy notification channel.
func (om *OrgMigration) createReceiver(channel *legacymodels.AlertNotification) (*apimodels.PostableApiReceiver, error) {
	if channel.Type == "hipchat" || channel.Type == "sensu" {
		return nil, fmt.Errorf("%s is a discontinued", channel.Type)
	}

	notifier, err := om.createNotifier(channel)
	if err != nil {
		return nil, err
	}

	return &apimodels.PostableApiReceiver{
		Receiver: config.Receiver{
			Name: channel.Name, // Channel name is unique within an Org.
		},
		PostableGrafanaReceivers: apimodels.PostableGrafanaReceivers{
			GrafanaManagedReceivers: []*apimodels.PostableGrafanaReceiver{notifier},
		},
	}, nil
}

// createBaseConfig creates an alertmanager config with the root-level route, default receiver, and nested route
// for migrated channels.
func createBaseConfig() (*apimodels.PostableUserConfig, *apimodels.Route) {
	defaultRoute, nestedRoute := createDefaultRoute()
	return &apimodels.PostableUserConfig{
		AlertmanagerConfig: apimodels.PostableApiAlertingConfig{
			Receivers: []*apimodels.PostableApiReceiver{
				{
					Receiver: config.Receiver{
						Name: "autogen-contact-point-default",
					},
					PostableGrafanaReceivers: apimodels.PostableGrafanaReceivers{
						GrafanaManagedReceivers: []*apimodels.PostableGrafanaReceiver{},
					},
				},
			},
			Config: apimodels.Config{
				Route: defaultRoute,
			},
		},
	}, nestedRoute
}

// createDefaultRoute creates a default root-level route and associated nested route that will contain all the migrated channels.
func createDefaultRoute() (*apimodels.Route, *apimodels.Route) {
	nestedRoute := createNestedLegacyRoute()
	return &apimodels.Route{
		Receiver:       "autogen-contact-point-default",
		Routes:         []*apimodels.Route{nestedRoute},
		GroupByStr:     []string{ngmodels.FolderTitleLabel, model.AlertNameLabel}, // To keep parity with pre-migration notifications.
		RepeatInterval: nil,
	}, nestedRoute
}

// createNestedLegacyRoute creates a nested route that will contain all the migrated channels.
// This route is matched on the UseLegacyChannelsLabel and mostly exists to keep the migrated channels separate and organized.
func createNestedLegacyRoute() *apimodels.Route {
	mat, _ := labels.NewMatcher(labels.MatchEqual, UseLegacyChannelsLabel, "true")
	return &apimodels.Route{
		ObjectMatchers: apimodels.ObjectMatchers{mat},
		Continue:       true,
	}
}

// createRoute creates a route from a legacy notification channel, and matches using a label based on the channel UID.
func createRoute(channel *legacymodels.AlertNotification, receiverName string) (*apimodels.Route, error) {
	// We create a matchers based on channel UID so that we only need a single route per channel.
	// All routes are stored in a nested route under the root. This is so we can keep the migrated channels separate
	// and organized.
	// The matchers are created using a label that is unique to the channel UID. So, each migrated alert rule can define
	// one label per migrated channel that it should send to.
	// Default channels are matched using a catch-all matcher, because in legacy alerting they are attached to all
	// alerts automatically.
	//
	// For example, if an alert needs to send to channel1 and channel2 it will have two labels:
	// - __contact_channel1__="true"
	// - __contact_channel2__="true"
	//
	// These will match two routes as they are all defined with Continue=true.

	label := fmt.Sprintf(ContactLabelTemplate, channel.UID)
	mat, err := labels.NewMatcher(labels.MatchEqual, label, "true")
	if err != nil {
		return nil, err
	}

	// If the channel is default, we create a catch-all matcher instead so this always matches.
	if channel.IsDefault {
		mat, err = labels.NewMatcher(labels.MatchRegexp, model.AlertNameLabel, ".+")
		if err != nil {
			return nil, err
		}
	}

	repeatInterval := DisabledRepeatInterval
	if channel.SendReminder {
		repeatInterval = model.Duration(channel.Frequency)
	}

	return &apimodels.Route{
		Receiver:       receiverName,
		ObjectMatchers: apimodels.ObjectMatchers{mat},
		Continue:       true, // We continue so that each sibling contact point route can separately match.
		RepeatInterval: &repeatInterval,
	}, nil
}

var secureKeysToMigrate = map[string][]string{
	"slack":                   {"url", "token"},
	"pagerduty":               {"integrationKey"},
	"webhook":                 {"password"},
	"prometheus-alertmanager": {"basicAuthPassword"},
	"opsgenie":                {"apiKey"},
	"telegram":                {"bottoken"},
	"line":                    {"token"},
	"pushover":                {"apiToken", "userKey"},
	"threema":                 {"api_secret"},
}

// Some settings were migrated from settings to secure settings in between.
// See https://grafana.com/docs/grafana/latest/installation/upgrading/#ensure-encryption-of-existing-alert-notification-channel-secrets.
// migrateSettingsToSecureSettings takes care of that.
func (om *OrgMigration) migrateSettingsToSecureSettings(chanType string, settings *simplejson.Json, secureSettings SecureJsonData) (*simplejson.Json, map[string]string, error) {
	keys := secureKeysToMigrate[chanType]
	newSecureSettings := secureSettings.Decrypt()
	cloneSettings := simplejson.New()
	settingsMap, err := settings.Map()
	if err != nil {
		return nil, nil, err
	}
	for k, v := range settingsMap {
		cloneSettings.Set(k, v)
	}
	for _, k := range keys {
		if v, ok := newSecureSettings[k]; ok && v != "" {
			continue
		}

		sv := cloneSettings.Get(k).MustString()
		if sv != "" {
			newSecureSettings[k] = sv
			cloneSettings.Del(k)
		}
	}

	err = om.encryptSecureSettings(newSecureSettings)
	if err != nil {
		return nil, nil, err
	}

	return cloneSettings, newSecureSettings, nil
}

func (om *OrgMigration) encryptSecureSettings(secureSettings map[string]string) error {
	for key, value := range secureSettings {
		encryptedData, err := om.encryptionService.Encrypt(context.Background(), []byte(value), secrets.WithoutScope())
		if err != nil {
			return fmt.Errorf("failed to encrypt secure settings: %w", err)
		}
		secureSettings[key] = base64.StdEncoding.EncodeToString(encryptedData)
	}
	return nil
}

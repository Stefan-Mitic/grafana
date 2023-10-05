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
	ngmodels "github.com/grafana/grafana/pkg/services/ngalert/models"
	"github.com/grafana/grafana/pkg/services/secrets"
)

const (
	// DisabledRepeatInterval is a large duration that will be used as a pseudo-disable in case a legacy channel doesn't have SendReminders enabled.
	DisabledRepeatInterval = model.Duration(time.Duration(8736) * time.Hour) // 1y
)

// channelReceiver is a convenience struct that contains a notificationChannel and its corresponding migrated PostableApiReceiver.
type channelReceiver struct {
	channel  *legacymodels.AlertNotification
	receiver *apimodels.PostableApiReceiver
}

// setupAlertmanagerConfigs creates Alertmanager configs with migrated receivers and routes.
func (om *orgMigration) setupAlertmanagerConfigs(ctx context.Context) (*apimodels.PostableUserConfig, error) {
	channels, err := om.getNotificationChannels(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load notification channels: %w", err)
	}

	amConfig, err := om.createBaseConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to create base alertmanager config in orgId %d: %w", om.orgID, err)
	}

	// Nested route that will contain all the migrated channels. This route is matched on the UseLegacyChannelsLabel
	// and mostly exists to keep the migrated channels separate and organized.
	nestedLegacyChannelRoute := amConfig.AlertmanagerConfig.Route.Routes[0]

	// Create all newly migrated receivers from legacy notification channels.
	for _, c := range channels {
		cr, err := om.createReceiver(c)
		if err != nil {
			return nil, fmt.Errorf("failed to create receiver for channel %s in orgId %d: %w", c.Name, om.orgID, err)
		}

		route, err := createRoute(cr)
		if err != nil {
			return nil, fmt.Errorf("failed to create route for receiver %s in orgId %d: %w", cr.receiver.Name, om.orgID, err)
		}

		nestedLegacyChannelRoute.Routes = append(nestedLegacyChannelRoute.Routes, route)
		amConfig.AlertmanagerConfig.Receivers = append(amConfig.AlertmanagerConfig.Receivers, cr.receiver)
	}

	return amConfig, nil
}

// validateAlertmanagerConfig validates the alertmanager configuration produced by the migration against the receivers.
func (om *orgMigration) validateAlertmanagerConfig(config *apimodels.PostableUserConfig) error {
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

// getNotificationChannels returns all channels for this org.
func (om *orgMigration) getNotificationChannels(ctx context.Context) ([]*legacymodels.AlertNotification, error) {
	alertNotifications, err := om.migrationStore.GetNotificationChannels(ctx, om.orgID)
	if err != nil {
		return nil, err
	}

	if len(alertNotifications) == 0 {
		return nil, nil
	}

	var channels []*legacymodels.AlertNotification
	for i, c := range alertNotifications {
		if c.Type == "hipchat" || c.Type == "sensu" {
			om.log.Error("Alert migration error: discontinued notification channel found", "type", c.Type, "name", c.Name, "uid", c.UID)
			continue
		}

		channels = append(channels, &alertNotifications[i])
	}
	return channels, nil
}

// createNotifier creates a PostableGrafanaReceiver from a legacy notification channel.
func (om *orgMigration) createNotifier(c *legacymodels.AlertNotification) (*apimodels.PostableGrafanaReceiver, error) {
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
func (om *orgMigration) createReceiver(channel *legacymodels.AlertNotification) (*channelReceiver, error) {
	notifier, err := om.createNotifier(channel)
	if err != nil {
		return nil, err
	}

	cr := channelReceiver{
		channel: channel,
		receiver: &apimodels.PostableApiReceiver{
			Receiver: config.Receiver{
				Name: channel.Name, // Channel name is unique within an Org.
			},
			PostableGrafanaReceivers: apimodels.PostableGrafanaReceivers{
				GrafanaManagedReceivers: []*apimodels.PostableGrafanaReceiver{notifier},
			},
		},
	}
	return &cr, nil
}

// createBaseConfig creates an alertmanager config with the root-level route, default receiver, and nested route
// for migrated channels.
func (om *orgMigration) createBaseConfig() (*apimodels.PostableUserConfig, error) {
	mat, err := labels.NewMatcher(labels.MatchEqual, UseLegacyChannelsLabel, "true")
	if err != nil {
		return nil, err
	}

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
				Route: &apimodels.Route{
					Receiver: "autogen-contact-point-default",
					Routes: []*apimodels.Route{{
						ObjectMatchers: apimodels.ObjectMatchers{mat},
						Continue:       true,
					}},
					GroupByStr:     []string{ngmodels.FolderTitleLabel, model.AlertNameLabel}, // To keep parity with pre-migration notifications.
					RepeatInterval: nil,
				},
			},
		},
	}, nil
}

// createRoute creates a route from a legacy notification channel, and matches using a label based on the channel UID.
func createRoute(cr *channelReceiver) (*apimodels.Route, error) {
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

	label := fmt.Sprintf(ContactLabelTemplate, cr.channel.UID)
	mat, err := labels.NewMatcher(labels.MatchEqual, label, "true")
	if err != nil {
		return nil, err
	}

	// If the channel is default, we create a catch-all matcher instead so this always matches.
	if cr.channel.IsDefault {
		mat, err = labels.NewMatcher(labels.MatchRegexp, model.AlertNameLabel, ".+")
		if err != nil {
			return nil, err
		}
	}

	repeatInterval := DisabledRepeatInterval
	if cr.channel.SendReminder {
		repeatInterval = model.Duration(cr.channel.Frequency)
	}

	return &apimodels.Route{
		Receiver:       cr.receiver.Name,
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
func (om *orgMigration) migrateSettingsToSecureSettings(chanType string, settings *simplejson.Json, secureSettings SecureJsonData) (*simplejson.Json, map[string]string, error) {
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

func (om *orgMigration) encryptSecureSettings(secureSettings map[string]string) error {
	for key, value := range secureSettings {
		encryptedData, err := om.encryptionService.Encrypt(context.Background(), []byte(value), secrets.WithoutScope())
		if err != nil {
			return fmt.Errorf("failed to encrypt secure settings: %w", err)
		}
		secureSettings[key] = base64.StdEncoding.EncodeToString(encryptedData)
	}
	return nil
}

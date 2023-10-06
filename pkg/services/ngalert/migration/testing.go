package migration

import (
	"testing"

	pb "github.com/prometheus/alertmanager/silence/silencepb"
	"github.com/stretchr/testify/require"

	"github.com/grafana/grafana/pkg/infra/log/logtest"
	"github.com/grafana/grafana/pkg/infra/serverlock"
	"github.com/grafana/grafana/pkg/infra/tracing"
	"github.com/grafana/grafana/pkg/services/accesscontrol"
	"github.com/grafana/grafana/pkg/services/folder"
	migmodels "github.com/grafana/grafana/pkg/services/ngalert/migration/models"
	migStore "github.com/grafana/grafana/pkg/services/ngalert/migration/store"
	fake_secrets "github.com/grafana/grafana/pkg/services/secrets/fakes"
	"github.com/grafana/grafana/pkg/services/sqlstore"
	"github.com/grafana/grafana/pkg/services/sqlstore/migrator"
	"github.com/grafana/grafana/pkg/setting"
)

// newTestOrgMigration generates an empty OrgMigration to use in tests.
func newTestOrgMigration(t *testing.T, orgID int64) *OrgMigration {
	t.Helper()
	return &OrgMigration{
		orgID: orgID,
		log:   &logtest.Fake{},

		dialect:           migrator.NewMysqlDialect(),
		encryptionService: fake_secrets.NewFakeSecretsService(),

		folderHelper: folderHelper{
			dialect:               migrator.NewMysqlDialect(),
			orgID:                 orgID,
			permissionsMap:        make(map[int64]map[permissionHash]*folder.Folder),
			folderCache:           make(map[int64]*folder.Folder),
			folderPermissionCache: make(map[string][]accesscontrol.ResourcePermission),
		},

		// We deduplicate for case-insensitive matching in MySQL-compatible backend flavours because they use case-insensitive collation.
		seenUIDs:            Deduplicator{set: make(map[string]struct{}), caseInsensitive: migrator.NewMysqlDialect().SupportEngine()},
		silences:            make([]*pb.MeshSilence, 0),
		alertRuleTitleDedup: make(map[string]Deduplicator),
		state: &migmodels.OrgMigrationState{
			OrgID: orgID,
		},
	}
}

func NewTestMigrationService(t testing.TB, sqlStore *sqlstore.SQLStore, cfg *setting.Cfg) *MigrationService {
	migrationStore := migStore.NewTestMigrationStore(t, sqlStore, cfg)

	ms, err := ProvideService(
		serverlock.ProvideService(sqlStore, tracing.InitializeTracerForTest()),
		cfg,
		sqlStore,
		migrationStore,
		fake_secrets.NewFakeSecretsService(),
		migrationStore.DashboardPermissions(),
	)
	require.NoError(t, err)
	return ms
}

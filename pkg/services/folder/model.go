package folder

import (
	"fmt"
	"time"

	"github.com/grafana/grafana/pkg/infra/slugify"
	"github.com/grafana/grafana/pkg/services/auth/identity"
	"github.com/grafana/grafana/pkg/setting"
	"github.com/grafana/grafana/pkg/util/errutil"
)

var ErrMaximumDepthReached = errutil.BadRequest("folder.maximum-depth-reached", errutil.WithPublicMessage("Maximum nested folder depth reached"))
var ErrBadRequest = errutil.BadRequest("folder.bad-request")
var ErrDatabaseError = errutil.Internal("folder.database-error")
var ErrInternal = errutil.Internal("folder.internal")
var ErrCircularReference = errutil.BadRequest("folder.circular-reference", errutil.WithPublicMessage("Circular reference detected"))
var ErrTargetRegistrySrvConflict = errutil.Internal("folder.target-registry-srv-conflict")

const (
	GeneralFolderUID     = "general"
	RootFolderUID        = ""
	MaxNestedFolderDepth = 8
)

var ErrFolderNotFound = errutil.NotFound("folder.notFound")

type Folder struct {
	ID          int64  `xorm:"pk autoincr 'id'"`
	OrgID       int64  `xorm:"org_id"`
	UID         string `xorm:"uid"`
	ParentUID   string `xorm:"parent_uid"`
	Title       string
	Description string

	Created time.Time
	Updated time.Time

	// TODO: validate if this field is required/relevant to folders.
	// currently there is no such column
	Version   int
	URL       string
	UpdatedBy int64
	CreatedBy int64
	HasACL    bool
}

var GeneralFolder = Folder{ID: 0, Title: "General"}

func (f *Folder) IsGeneral() bool {
	return f.ID == GeneralFolder.ID && f.Title == GeneralFolder.Title
}

func (f *Folder) WithURL() *Folder {
	if f == nil || f.URL != "" {
		return f
	}

	// copy of dashboards.GetFolderURL()
	f.URL = fmt.Sprintf("%s/dashboards/f/%s/%s", setting.AppSubUrl, f.UID, slugify.Slugify(f.Title))
	return f
}

// NewFolder tales a title and returns a Folder with the Created and Updated
// fields set to the current time.
func NewFolder(title string, description string) *Folder {
	return &Folder{
		Title:       title,
		Description: description,
		Created:     time.Now(),
		Updated:     time.Now(),
	}
}

// CreateFolderCommand captures the information required by the folder service
// to create a folder.
type CreateFolderCommand struct {
	UID         string `json:"uid"`
	OrgID       int64  `json:"-"`
	Title       string `json:"title"`
	Description string `json:"description"`
	ParentUID   string `json:"parentUid"`

	SignedInUser identity.Requester `json:"-"`
}

// UpdateFolderCommand captures the information required by the folder service
// to update a folder. Use Move to update a folder's parent folder.
type UpdateFolderCommand struct {
	UID   string `json:"-"`
	OrgID int64  `json:"-"`
	// NewTitle it's an optional parameter used for overriding the existing folder title
	NewTitle *string `json:"title"` // keep same json tag with the legacy command for not breaking the existing APIs
	// NewDescription it's an optional parameter used for overriding the existing folder description
	NewDescription *string `json:"description"` // keep same json tag with the legacy command for not breaking the existing APIs
	NewParentUID   *string `json:"-"`

	// Version only used by the legacy folder implementation
	Version int `json:"version"`
	// Overwrite only used by the legacy folder implementation
	Overwrite bool `json:"overwrite"`

	SignedInUser identity.Requester `json:"-"`
}

// MoveFolderCommand captures the information required by the folder service
// to move a folder.
type MoveFolderCommand struct {
	UID          string `json:"-"`
	NewParentUID string `json:"parentUid"`
	OrgID        int64  `json:"-"`

	SignedInUser identity.Requester `json:"-"`
}

// DeleteFolderCommand captures the information required by the folder service
// to delete a folder.
type DeleteFolderCommand struct {
	UID              string `json:"uid" xorm:"uid"`
	OrgID            int64  `json:"orgId" xorm:"org_id"`
	ForceDeleteRules bool   `json:"forceDeleteRules"`

	SignedInUser identity.Requester `json:"-"`
}

// GetFolderQuery is used for all folder Get requests. Only one of UID, ID, or
// Title should be set; if multiple fields are set by the caller the dashboard
// service will select the field with the most specificity, in order: UID, ID
// Title. If Title is set, it will fetch the folder in the root folder.
// Callers can additionally set the ParentUID field to fetch a folder by title under a specific folder.
type GetFolderQuery struct {
	UID       *string
	ParentUID *string
	ID        *int64
	Title     *string
	OrgID     int64

	SignedInUser identity.Requester `json:"-"`
}

// GetParentsQuery captures the information required by the folder service to
// return a list of all parent folders of a given folder.
type GetParentsQuery struct {
	UID   string `xorm:"uid"`
	OrgID int64  `xorm:"org_id"`
}

// GetChildrenQuery captures the information required by the folder service to
// return a list of child folders of the given folder.

type GetChildrenQuery struct {
	UID   string `xorm:"uid"`
	OrgID int64  `xorm:"org_id"`
	Depth int64

	// Pagination options
	Limit int64
	Page  int64

	SignedInUser identity.Requester `json:"-"`
}

type HasEditPermissionInFoldersQuery struct {
	SignedInUser identity.Requester
}

type HasAdminPermissionInDashboardsOrFoldersQuery struct {
	SignedInUser identity.Requester
}

// GetDescendantCountsQuery captures the information required by the folder service
// to return the count of descendants (direct and indirect) in a folder.
type GetDescendantCountsQuery struct {
	UID   *string
	OrgID int64

	SignedInUser identity.Requester `json:"-"`
}

type DescendantCounts map[string]int64

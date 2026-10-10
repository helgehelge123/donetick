package circle

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"donetick.com/core/config"
	chModel "donetick.com/core/internal/chore/model"
	chRepo "donetick.com/core/internal/chore/repo"
	cModel "donetick.com/core/internal/circle/model"
	cRepo "donetick.com/core/internal/circle/repo"
	"donetick.com/core/internal/database"
	nModel "donetick.com/core/internal/notifier/model"
	pModel "donetick.com/core/internal/project/model"
	pjRepo "donetick.com/core/internal/project/repo"
	uModel "donetick.com/core/internal/user/model"
)

// A colleague who created tasks inside a project shared with them leaves the
// circle. Upstream moves every chore a leaving user created into their home
// circle; for a chore of a restricted project that strands it (nobody can see a
// restricted project's chore in another circle). Instead it stays with the
// project and its owner takes it over.
func TestLeavingCircleKeepsSharedProjectTasksWithTheOwner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.Migration(db))
	cfg := &config.Config{}
	cfg.Database.Type = "sqlite"
	choreRepository := chRepo.NewChoreRepository(db, cfg)
	h := &Handler{choreRepo: choreRepository, circleRepo: cRepo.NewCircleRepository(db), projectRepo: pjRepo.NewProjectRepository(db, cfg, choreRepository)}

	const workCircle, homeCircle, owner, colleague = 1, 2, 1, 2
	require.NoError(t, db.Create(&cModel.Circle{ID: workCircle, Name: "work", CreatedBy: owner, InviteCode: "w"}).Error)
	require.NoError(t, db.Create(&cModel.Circle{ID: homeCircle, Name: "home", CreatedBy: colleague, InviteCode: "h"}).Error)
	require.NoError(t, db.Create(&cModel.UserCircle{UserID: owner, CircleID: workCircle, IsActive: true, Role: cModel.UserRoleAdmin}).Error)
	require.NoError(t, db.Create(&cModel.UserCircle{UserID: colleague, CircleID: workCircle, IsActive: true, Role: cModel.UserRoleMember}).Error)
	require.NoError(t, db.Create(&cModel.UserCircle{UserID: colleague, CircleID: homeCircle, IsActive: true, Role: cModel.UserRoleAdmin}).Error)

	shared := &pModel.Project{Name: "Installation A", CircleID: workCircle, CreatedBy: owner, IsPrivate: true, SharedWithMembers: true, MemberIDs: []int{colleague}}
	require.NoError(t, h.projectRepo.CreateProject(t.Context(), shared))
	household := &pModel.Project{Name: "Haushalt", CircleID: workCircle, CreatedBy: owner}
	require.NoError(t, h.projectRepo.CreateProject(t.Context(), household))
	ownProject := &pModel.Project{Name: "colleague's own", CircleID: workCircle, CreatedBy: colleague, IsPrivate: true, SharedWithMembers: true, MemberIDs: []int{owner}}
	require.NoError(t, h.projectRepo.CreateProject(t.Context(), ownProject))

	inShared := &chModel.Chore{Name: "Kabel bestellen", CircleID: workCircle, CreatedBy: colleague, ProjectID: &shared.ID, IsPrivate: true, IsActive: true,
		AssignStrategy: chModel.AssignmentStrategyRandom, AssignedTo: ptr(colleague), Assignees: []chModel.ChoreAssignees{{UserID: colleague}}}
	inHousehold := &chModel.Chore{Name: "Müll", CircleID: workCircle, CreatedBy: colleague, ProjectID: &household.ID, IsActive: true,
		AssignStrategy: chModel.AssignmentStrategyRandom, AssignedTo: ptr(owner), Assignees: []chModel.ChoreAssignees{{UserID: owner}}}
	ownersInShared := &chModel.Chore{Name: "Server prüfen", CircleID: workCircle, CreatedBy: owner, ProjectID: &shared.ID, IsPrivate: true, IsActive: true,
		AssignStrategy: chModel.AssignmentStrategyRoundRobin, AssignedTo: ptr(colleague), Assignees: []chModel.ChoreAssignees{{UserID: owner}, {UserID: colleague}}}
	byOwnerInColleagues := &chModel.Chore{Name: "in colleague's project", CircleID: workCircle, CreatedBy: owner, ProjectID: &ownProject.ID, IsPrivate: true, IsActive: true,
		AssignStrategy: chModel.AssignmentStrategyRandom, AssignedTo: ptr(owner), Assignees: []chModel.ChoreAssignees{{UserID: owner}}}
	for _, ch := range []*chModel.Chore{inShared, inHousehold, ownersInShared, byOwnerInColleagues} {
		require.NoError(t, db.Create(ch).Error)
	}
	require.NoError(t, db.Create(&nModel.Notification{ChoreID: inShared.ID, CircleID: workCircle, UserID: colleague, Text: "reminder"}).Error)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	leaving := &uModel.User{ID: colleague, CircleID: workCircle}
	require.NoError(t, handleUserLeavingCircle(h, c, leaving, homeCircle))

	reload := func(id int) *chModel.Chore {
		var ch chModel.Chore
		require.NoError(t, db.Preload("Assignees").First(&ch, id).Error)
		return &ch
	}
	// The task in the shared project stays in the work circle with the owner.
	got := reload(inShared.ID)
	require.Equal(t, workCircle, got.CircleID)
	require.Equal(t, shared.ID, *got.ProjectID)
	require.Equal(t, owner, got.CreatedBy)
	require.Equal(t, owner, *got.AssignedTo)
	require.Equal(t, []int{owner}, assigneeIDs(got))
	visible, err := choreRepository.GetChore(t.Context(), inShared.ID, owner, workCircle)
	require.NoError(t, err)
	require.Equal(t, "Kabel bestellen", visible.Name)

	// The task in the circle-wide project moves with the colleague (upstream rule),
	// but without a project reference that only exists in the old circle.
	got = reload(inHousehold.ID)
	require.Equal(t, homeCircle, got.CircleID)
	require.Nil(t, got.ProjectID)
	require.Equal(t, colleague, *got.AssignedTo)

	// The owner's task loses the colleague as assignee and current assignee.
	got = reload(ownersInShared.ID)
	require.Equal(t, workCircle, got.CircleID)
	require.Equal(t, []int{owner}, assigneeIDs(got))
	require.Equal(t, owner, *got.AssignedTo)

	// The colleague's own shared project is gone; the owner's task in it is kept,
	// detached, and still visible to the owner.
	var remaining int64
	require.NoError(t, db.Model(&pModel.Project{}).Where("id = ?", ownProject.ID).Count(&remaining).Error)
	require.Zero(t, remaining)
	got = reload(byOwnerInColleagues.ID)
	require.Nil(t, got.ProjectID)
	_, err = choreRepository.GetChore(t.Context(), byOwnerInColleagues.ID, owner, workCircle)
	require.NoError(t, err)

	// No pending reminders and no project shares are left for the colleague.
	var pending, shares int64
	require.NoError(t, db.Model(&nModel.Notification{}).Where("user_id = ? AND circle_id = ? AND is_sent = ?", colleague, workCircle, false).Count(&pending).Error)
	require.Zero(t, pending)
	require.NoError(t, db.Model(&pModel.ProjectMember{}).Where("user_id = ? AND project_id = ?", colleague, shared.ID).Count(&shares).Error)
	require.EqualValues(t, 1, shares, "the share row is removed by the repository's leave call, which the handler runs separately")
}

func ptr(v int) *int { return &v }

func assigneeIDs(ch *chModel.Chore) []int {
	ids := make([]int, 0, len(ch.Assignees))
	for _, a := range ch.Assignees {
		ids = append(ids, a.UserID)
	}
	return ids
}

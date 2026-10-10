# Fork notes: project sharing with selected members

This branch (`helge/project-sharing`) is based on the `v0.1.80` release tag and
adds one feature on top of upstream's private projects (donetick/donetick#749,
#851): a project can be shared with selected members of the same circle.

## Visibility model

| UI label               | API `visibility` | `is_private` | `shared_with_members` | Who sees the project and every task in it |
|------------------------|------------------|--------------|-----------------------|-------------------------------------------|
| Only me                | `private`        | true         | false                 | the owner                                 |
| Selected members       | `members`        | true         | true                  | the owner + rows in `project_members`     |
| Whole circle           | `circle`         | false        | false                 | every member of the circle                |

* `visibility` is derived, never stored. A database written by this fork and
  read by an upstream binary shows a "members" project as owner-only, so a
  rollback can never widen access.
* Upgrading an existing database only adds the `shared_with_members` column
  (default `false`) and the empty `project_members` table: every existing
  project keeps exactly its previous audience.
* Member rows only count while `shared_with_members` is true, and they are
  deleted whenever a project leaves the "members" visibility, when the project
  is deleted, when the user leaves or is removed from the circle, and when the
  account is deleted.
* Circle admins and managers do not bypass a restricted project (same rule as
  upstream private projects).

## API

* `GET /api/v1/projects` returns `visibility` and, for "members" projects,
  `memberIds`.
* `POST /api/v1/projects`, `PUT /api/v1/projects/{id}` accept `visibility`
  (`private|members|circle`) and `memberIds`. Fields that are not sent keep
  their value. Clients that only know `isPrivate` keep a "members" project
  shared as long as they send `isPrivate: true`; `isPrivate: false` opens it to
  the circle. Only the owner may change a project.
* `POST /api/v1/projects/{id}/access-preview` returns, without writing, which
  tasks change assignees and who gains or loses access for a given target.

## Enforcement (server side)

* All chore reads go through `privacyJoins`/`privacyPredicate`
  (`internal/chore/repo/repository.go`), which now also joins the user's
  `project_members` row: lists, single task, details, archived, history,
  delta sync. `Chore.CanView`/`CanComplete` follow the project audience.
* Creating or moving a task into a "members" project rejects assignees outside
  the audience; "Anyone" rotates and notifies only within the audience.
* Changing a project's audience (`ApplyProjectAccessChange`) trims assignees
  who lose access (the owner takes over a task that would otherwise end up
  with nobody), removes pending reminders and regenerates them after commit,
  writes user-scoped tombstones for the task **and its history** for everyone
  who loses access, re-versions history for everyone who gains access and
  deletes their stale tombstones. Completion history is never deleted.
* Realtime: task events go only to the audience; users who lose access get a
  `chore.deleted` event; `project.updated` tells clients to refresh.
* Reminders of restricted projects never go to the circle group chat or the
  circle webhook; the external API (`/eapi`) checks project visibility.
* Impersonation (`X-Impersonate-User-ID`, available to circle admins and
  managers for any member) cannot reach into a chore the actual user could not
  see themselves; acting for one's own sub-account stays allowed
  (`impersonationGuard` in the chore handler).
* When a member leaves or is removed from the circle, chores they created inside
  a restricted project stay with the project and its owner takes them over
  (`HandOverToProjectOwner`); upstream would move them into the member's home
  circle, where nobody can see a restricted project's chore. Chores moved along
  lose their project reference (the project belongs to the old circle). Projects
  the leaving user owned are removed, their remaining chores detached and kept.
  Account deletion removes the user's projects the same way.
* Thing triggers only reschedule chores their owner can still see.

## Known trade-offs

* Members of a shared project see its full member list (`memberIds`); nobody
  outside the audience does.
* Revoking access writes one user-scoped tombstone per task and per history
  entry of that task, so revoked clients drop both. Large projects with many
  completions produce correspondingly many rows; `tombstones` is never pruned.
* Signed attachment links stay valid until they expire, also after a revocation.

## Behaviour changes compared to upstream v0.1.80

* `TestGetChangesReturnsRevocationBeforeNewerRegrant` became
  `TestGetChangesDropsRevocationSupersededByRegrant`: a re-grant now deletes
  the user's stale tombstone, because clients apply deletions after upserts and
  would otherwise drop a task they can see again.
* Project privacy changes no longer delete reminders without regenerating them.
* Archiving/unarchiving requires that the user can still see the task.
* Deleting a project regenerates the reminders of chores whose audience shrank.
* `circle.NewHandler` takes the project repository as an additional argument.

Tests for all of the above: `go test ./...` (see `*members*_test.go`,
`visibility_test.go`, `migration_visibility_test.go`).

## Moving to a new upstream release

`update/upstream-update.sh <tag>` (in the deployment folder) rebases this branch
onto the tag and the frontend branch onto the matching upstream `develop` commit.
Things that have needed attention so far:

* Upstream tests that `AutoMigrate` a hand-picked list of models (for example
  `internal/sync/handler_test.go`, `internal/chore/repo/tombstone_test.go`,
  `internal/chore/api_permissions_test.go` in v0.1.81) fail with
  `no such table: project_members` because `privacyJoins` joins that table. Add
  `&pModel.ProjectMember{}` to their list.
* Every new read path over chores must use `ChoreRepository.GetChores`/`GetChore`
  (or `privacyJoins` + `privacyPredicate`). Checked for v0.1.81-beta.4: the new
  iCal endpoint (`internal/calendar`) uses `GetChores` with the token's user, and
  the delegate handler only accepts existing assignees, so both stay inside the
  project audience.
* `Chore.CanView`/`CanComplete` and `checkNextAssignee` carry the project rule;
  keep that when upstream edits those functions (v0.1.81 adds `CanDelegate`
  right next to them).

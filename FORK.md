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

## Behaviour changes compared to upstream v0.1.80

* `TestGetChangesReturnsRevocationBeforeNewerRegrant` became
  `TestGetChangesDropsRevocationSupersededByRegrant`: a re-grant now deletes
  the user's stale tombstone, because clients apply deletions after upserts and
  would otherwise drop a task they can see again.
* Project privacy changes no longer delete reminders without regenerating them.
* Archiving/unarchiving requires that the user can still see the task.

Tests for all of the above: `go test ./...` (see `*members*_test.go`,
`visibility_test.go`, `migration_visibility_test.go`).

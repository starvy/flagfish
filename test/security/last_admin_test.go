//go:build integration

package security

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/starvy/flagfish/internal/adminops"
	"github.com/starvy/flagfish/internal/audit"
	"github.com/starvy/flagfish/internal/domain/account"
)

// The last usable admin is an instance-availability invariant, and every route that can cost
// the instance its last one has to serialize against every other such route — a demotion, an
// account ban, a team ban, and moving an admin onto a banned team all decide by counting who
// would be left, and two counts taken outside one lock each see the other's admin as the
// survivor. These cases drive the pairs concurrently and assert on the rows, not on which call
// happened to win: after the dust settles, at least one admin can still log in.

// usableAdmins counts the admins who could still reach an admin route — unbanned, and not sitting
// on a banned team. It is the same predicate the auth layer gates on, read straight from the rows.
func (f *fixture) usableAdmins() int64 {
	return f.count(`
		SELECT count(*)
		  FROM users u
		  LEFT JOIN teams t ON t.id = u.team_id
		 WHERE u.role = 'admin'
		   AND u.banned = false
		   AND COALESCE(t.banned, false) = false`)
}

func actor(id int64) audit.Actor { return audit.Actor{ID: id} }

// okOrLastAdmin is the only pair of outcomes a racing remove-admin call may have: it either won,
// or it was the one refused to keep an admin alive. Anything else — a serialization abort, a
// surprise error — is itself a failure, so the goroutines assert it rather than discarding err.
func okOrLastAdmin(t *testing.T, err error) {
	t.Helper()
	if err != nil && !errors.Is(err, adminops.ErrLastAdmin) {
		t.Errorf("unexpected error from a racing admin mutation: %v", err)
	}
}

// S40 — a demotion racing a ban cannot empty the admin set.
//
// Admin A demotes admin B while admin B bans admin A. Under the old code the ban took no lock, so
// both transactions counted the other as the admin that remains and both committed. With ban on
// the same lock as demotion, exactly one of the two wins and the other is refused ErrLastAdmin.
func TestS40_DemoteRacingBanKeepsAnAdmin(t *testing.T) {
	for i := range 12 {
		f := setup(t)
		ops := adminops.New(f.pool)
		a := f.user("admin-a", pw, asAdmin)
		b := f.user("admin-b", pw, asAdmin)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := ops.SetRole(context.Background(), actor(a), b, "user")
			okOrLastAdmin(t, err)
		}()
		go func() {
			defer wg.Done()
			_, err := ops.SetBanned(context.Background(), actor(b), a, true)
			okOrLastAdmin(t, err)
		}()
		wg.Wait()

		if got := f.usableAdmins(); got == 0 {
			t.Fatalf("run %d: the instance has no usable admin after a concurrent demote+ban", i)
		}
	}
}

// S41 — two bans racing at the last two admins cannot empty the set.
//
// Neither admin bans themselves (that is the case the self-ban refusal already covers); each bans
// the other. Serialized, the second ban must see the first already gone and be refused.
func TestS41_CrossBansKeepAnAdmin(t *testing.T) {
	for i := range 12 {
		f := setup(t)
		ops := adminops.New(f.pool)
		a := f.user("admin-a", pw, asAdmin)
		b := f.user("admin-b", pw, asAdmin)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := ops.SetBanned(context.Background(), actor(a), b, true)
			okOrLastAdmin(t, err)
		}()
		go func() {
			defer wg.Done()
			_, err := ops.SetBanned(context.Background(), actor(b), a, true)
			okOrLastAdmin(t, err)
		}()
		wg.Wait()

		if got := f.usableAdmins(); got == 0 {
			t.Fatalf("run %d: the instance has no usable admin after two crossed bans", i)
		}
	}
}

// S42 — a team ban racing a demotion cannot empty the set.
//
// A banned team walls its members out of every route, so banning the team holding admin B is a
// way to remove B that the old AdminCountOtherAdmins — filtering only banned, not team_banned —
// could not see. Admin A bans B's team while B demotes A.
func TestS42_TeamBanRacingDemoteKeepsAnAdmin(t *testing.T) {
	for i := range 12 {
		f := setup(t, withTeamsMode())
		ops := adminops.New(f.pool)

		aTeam := f.team("team-a")
		bTeam := f.team("team-b")
		a := f.user("admin-a", pw, asAdmin)
		b := f.user("admin-b", pw, asAdmin)
		f.assign(a, aTeam)
		f.assign(b, bTeam)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := ops.SetTeamBanned(context.Background(), actor(a), bTeam, true)
			okOrLastAdmin(t, err)
		}()
		go func() {
			defer wg.Done()
			_, err := ops.SetRole(context.Background(), actor(b), a, "user")
			okOrLastAdmin(t, err)
		}()
		wg.Wait()

		if got := f.usableAdmins(); got == 0 {
			t.Fatalf("run %d: the instance has no usable admin after a concurrent team-ban+demote", i)
		}
	}
}

// S43 — moving the last admin onto a banned team is refused outright.
//
// This one needs no race: the move route had no self-ban refusal and no admin count, so a single
// call could take the sole admin out of reach. It stays possible for a non-last admin (retiring a
// compromised account), and refused for the last.
func TestS43_MovingLastAdminOntoBannedTeamIsRefused(t *testing.T) {
	f := setup(t, withTeamsMode())
	ops := adminops.New(f.pool)
	ctx := context.Background()

	home := f.team("home")
	quarantine := f.team("quarantine")
	admin := f.user("only-admin", pw, asAdmin)
	f.assign(admin, home)

	// Ban the destination team via the service, so its own last-admin guard runs (a second admin
	// exists only for the duration of this ban, then is demoted away).
	guard := f.user("temp-admin", pw, asAdmin)
	if _, err := ops.SetTeamBanned(ctx, actor(guard), quarantine, true); err != nil {
		t.Fatalf("seed banned team: %v", err)
	}
	if _, err := ops.SetRole(ctx, actor(guard), guard, "user"); err != nil {
		t.Fatalf("demote temp admin: %v", err)
	}

	err := ops.MoveTeamMember(ctx, actor(admin), account.ModeTeams, home, admin, quarantine)
	if !errors.Is(err, adminops.ErrLastAdmin) {
		t.Fatalf("moving the last admin onto a banned team: %v, want ErrLastAdmin", err)
	}
	if got := f.usableAdmins(); got == 0 {
		t.Fatal("the move went through: no usable admin remains")
	}
}

// S44 — moving a non-last admin onto a banned team still works.
//
// The guard refuses only the move that would empty the set; retiring one compromised admin into a
// banned team while another remains is a legitimate act and must not be blocked.
func TestS44_MovingNonLastAdminOntoBannedTeamIsAllowed(t *testing.T) {
	f := setup(t, withTeamsMode())
	ops := adminops.New(f.pool)
	ctx := context.Background()

	home := f.team("home")
	quarantine := f.team("quarantine")
	keep := f.user("kept-admin", pw, asAdmin)
	f.assign(keep, home)
	retire := f.user("retired-admin", pw, asAdmin)
	f.assign(retire, home)

	// Ban the destination without spending either standing admin.
	seed := f.user("seed-admin", pw, asAdmin)
	if _, err := ops.SetTeamBanned(ctx, actor(seed), quarantine, true); err != nil {
		t.Fatalf("seed banned team: %v", err)
	}
	if _, err := ops.SetRole(ctx, actor(seed), seed, "user"); err != nil {
		t.Fatalf("demote seed admin: %v", err)
	}

	if err := ops.MoveTeamMember(ctx, actor(keep), account.ModeTeams, home, retire, quarantine); err != nil {
		t.Fatalf("moving a non-last admin onto a banned team was refused: %v", err)
	}
	if got := f.usableAdmins(); got != 1 {
		t.Fatalf("want exactly one usable admin left, got %d", got)
	}
}

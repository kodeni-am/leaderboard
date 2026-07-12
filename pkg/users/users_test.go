package users

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// testStore is the conformance suite every Store implementation must pass.
// app must be unique per run for stores with persistent backends.
func testStore(t *testing.T, s Store, app string) {
	ctx := context.Background()

	// Create trims whitespace, mints a plr_ id, and stores the display form.
	u, err := s.Create(ctx, app, "  Ninja  ", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u.ID, "plr_") || u.Nickname != "Ninja" {
		t.Fatalf("unexpected user: %+v", u)
	}
	if u.CreatedAt.IsZero() || u.UpdatedAt.IsZero() {
		t.Errorf("timestamps not set: %+v", u)
	}
	if got, err := s.Get(ctx, app, u.ID); err != nil || got.Nickname != "Ninja" {
		t.Fatalf("Get: %+v / %v", got, err)
	}
	if got, err := s.GetByNickname(ctx, app, "NINJA"); err != nil || got.ID != u.ID {
		t.Fatalf("GetByNickname is case-insensitive: %+v / %v", got, err)
	}

	// Uniqueness is case-insensitive within an app, and scoped per app.
	if _, err := s.Create(ctx, app, "ninja", ""); !errors.Is(err, ErrNicknameTaken) {
		t.Errorf("case-insensitive dup: got %v, want ErrNicknameTaken", err)
	}
	if _, err := s.Create(ctx, app+"other", "Ninja", ""); err != nil {
		t.Errorf("same nick in another app: %v", err)
	}

	// Invalid nicknames are rejected before any state changes.
	for _, bad := range []string{"", "   ", strings.Repeat("x", 33), "a\x00b", "line\nbreak", "a‮b", "Ninja​"} {
		if _, err := s.Create(ctx, app, bad, ""); !errors.Is(err, ErrInvalidNickname) {
			t.Errorf("Create(%q): got %v, want ErrInvalidNickname", bad, err)
		}
	}
	// 32 runes of multibyte characters are valid (rune count, not bytes).
	if _, err := s.Create(ctx, app, strings.Repeat("ü", 32), ""); err != nil {
		t.Errorf("32-rune multibyte nickname: %v", err)
	}

	// Unknown lookups.
	if _, err := s.Get(ctx, app, "plr_nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get unknown: %v", err)
	}
	if _, err := s.GetByNickname(ctx, app, "Ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetByNickname unknown: %v", err)
	}

	// Rename claims the new name and releases the old one.
	u2, err := s.Create(ctx, app, "Pixel", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Rename(ctx, app, u2.ID, "Ninja"); !errors.Is(err, ErrNicknameTaken) {
		t.Errorf("rename to taken: %v", err)
	}
	if _, err := s.Rename(ctx, app, "plr_nope", "Foo"); !errors.Is(err, ErrNotFound) {
		t.Errorf("rename unknown user: %v", err)
	}
	ren, err := s.Rename(ctx, app, u2.ID, "Voxel")
	if err != nil || ren.Nickname != "Voxel" {
		t.Fatalf("Rename: %+v / %v", ren, err)
	}
	if _, err := s.Create(ctx, app, "Pixel", ""); err != nil {
		t.Errorf("old name should be free after rename: %v", err)
	}
	if got, err := s.GetByNickname(ctx, app, "voxel"); err != nil || got.ID != u2.ID {
		t.Fatalf("new name resolves: %+v / %v", got, err)
	}
	// Case-only rename keeps the claim and updates the display form.
	if ren, err = s.Rename(ctx, app, u2.ID, "VOXEL"); err != nil || ren.Nickname != "VOXEL" {
		t.Fatalf("case-only rename: %+v / %v", ren, err)
	}

	// Batch nickname resolution skips unregistered ids.
	names, err := s.Nicknames(ctx, app, []string{u.ID, "raw-member", u2.ID})
	if err != nil {
		t.Fatal(err)
	}
	if names[u.ID] != "Ninja" || names[u2.ID] != "VOXEL" || len(names) != 2 {
		t.Fatalf("Nicknames: %v", names)
	}
	if names, err := s.Nicknames(ctx, app, nil); err != nil || len(names) != 0 {
		t.Fatalf("Nicknames(empty): %v / %v", names, err)
	}

	// Exactly one concurrent claimant can win a nickname.
	const claimants = 8
	errs := make(chan error, claimants)
	for i := 0; i < claimants; i++ {
		go func() {
			_, err := s.Create(ctx, app, "Contested", "")
			errs <- err
		}()
	}
	wins := 0
	for i := 0; i < claimants; i++ {
		if err := <-errs; err == nil {
			wins++
		} else if !errors.Is(err, ErrNicknameTaken) {
			t.Errorf("concurrent create: %v", err)
		}
	}
	if wins != 1 {
		t.Errorf("concurrent create: %d wins, want exactly 1", wins)
	}

	// Concurrent renames of the same player must not orphan nickname claims.
	vic, err := s.Create(ctx, app, "Racer", "")
	if err != nil {
		t.Fatal(err)
	}
	const renamers = 6
	done := make(chan error, renamers)
	for i := 0; i < renamers; i++ {
		name := fmt.Sprintf("Racer-%d", i)
		go func() {
			_, err := s.Rename(ctx, app, vic.ID, name)
			done <- err
		}()
	}
	for i := 0; i < renamers; i++ {
		if err := <-done; err != nil {
			t.Errorf("concurrent rename: %v", err)
		}
	}
	cur, err := s.Get(ctx, app, vic.ID)
	if err != nil {
		t.Fatal(err)
	}
	claims := 0
	for i := 0; i < renamers; i++ {
		name := fmt.Sprintf("Racer-%d", i)
		got, err := s.GetByNickname(ctx, app, name)
		switch {
		case err == nil:
			claims++
			if got.ID != vic.ID || !strings.EqualFold(cur.Nickname, name) {
				t.Errorf("claim %q inconsistent: maps to %s, record nick %q", name, got.ID, cur.Nickname)
			}
		case !errors.Is(err, ErrNotFound):
			t.Errorf("GetByNickname(%q): %v", name, err)
		}
	}
	if claims != 1 {
		t.Errorf("player holds %d nickname claims, want exactly 1", claims)
	}
	if _, err := s.GetByNickname(ctx, app, "Racer"); !errors.Is(err, ErrNotFound) {
		t.Errorf("original nickname not released: %v", err)
	}

	// Delete removes the registration and releases the nickname for re-claim.
	del, err := s.Create(ctx, app, "Vanish", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, app, del.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, app, del.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after delete: %v, want ErrNotFound", err)
	}
	if n, err := s.Nicknames(ctx, app, []string{del.ID}); err != nil || len(n) != 0 {
		t.Errorf("Nicknames after delete: %v / %v", n, err)
	}
	reclaimed, err := s.Create(ctx, app, "Vanish", "")
	if err != nil {
		t.Fatalf("nickname not released: %v", err)
	}

	// Deleting an unknown id is a no-op (idempotent).
	if err := s.Delete(ctx, app, "plr_nope"); err != nil {
		t.Errorf("Delete unknown: %v, want nil", err)
	}

	// Replayed delete of the OLD id must not touch the re-claimed nickname:
	// the claim now maps to reclaimed.ID, not del.ID.
	if err := s.Delete(ctx, app, del.ID); err != nil {
		t.Errorf("replayed delete: %v", err)
	}
	if got, err := s.GetByNickname(ctx, app, "vanish"); err != nil || got.ID != reclaimed.ID {
		t.Errorf("re-claimed nickname lost after replayed delete: %+v / %v", got, err)
	}

	// Create with an explicit member id claims that id in place of a minted
	// one — the same string games already submit as the board member. The id
	// is trimmed like nicknames are.
	cl, err := s.Create(ctx, app, "Surfer", "  surfer-abc123  ")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if cl.ID != "surfer-abc123" || cl.Nickname != "Surfer" {
		t.Fatalf("claim: %+v", cl)
	}
	if got, err := s.Get(ctx, app, "surfer-abc123"); err != nil || got.Nickname != "Surfer" {
		t.Fatalf("Get claimed: %+v / %v", got, err)
	}
	if got, err := s.GetByNickname(ctx, app, "SURFER"); err != nil || got.ID != "surfer-abc123" {
		t.Fatalf("GetByNickname claimed: %+v / %v", got, err)
	}
	if names, err := s.Nicknames(ctx, app, []string{"surfer-abc123"}); err != nil || names["surfer-abc123"] != "Surfer" {
		t.Fatalf("Nicknames claimed: %v / %v", names, err)
	}

	// A member id can be registered only once; the error is distinct from
	// nickname_taken so clients can tell the two conflicts apart.
	if _, err := s.Create(ctx, app, "SomeoneElse", "surfer-abc123"); !errors.Is(err, ErrMemberTaken) {
		t.Errorf("re-claim: got %v, want ErrMemberTaken", err)
	}

	// A losing nickname claim with an explicit id must not leave partial state.
	if _, err := s.Create(ctx, app, "surfer", "surfer-loser"); !errors.Is(err, ErrNicknameTaken) {
		t.Errorf("claim with taken nickname: got %v, want ErrNicknameTaken", err)
	}
	if _, err := s.Get(ctx, app, "surfer-loser"); !errors.Is(err, ErrNotFound) {
		t.Errorf("losing claim left a record: %v", err)
	}

	// Invalid member ids are rejected; plr_ is the server-minted namespace and
	// can never be occupied by a claim. A rejected id must not consume the
	// nickname, and 64 runes (the cap) is still valid.
	for _, bad := range []string{"   ", strings.Repeat("x", 65), "a\x00b", "line\nbreak", "plr_impostor"} {
		if _, err := s.Create(ctx, app, "FreshNick", bad); !errors.Is(err, ErrInvalidMember) {
			t.Errorf("Create(member=%q): got %v, want ErrInvalidMember", bad, err)
		}
	}
	if _, err := s.Create(ctx, app, "FreshNick", strings.Repeat("y", 64)); err != nil {
		t.Errorf("64-rune member id: %v", err)
	}

	// Rename and delete treat a claimed raw id like any player; delete
	// releases the nickname and the id becomes claimable again.
	ren2, err := s.Rename(ctx, app, "surfer-abc123", "WaveLord")
	if err != nil || ren2.Nickname != "WaveLord" || ren2.ID != "surfer-abc123" {
		t.Fatalf("rename claimed: %+v / %v", ren2, err)
	}
	if err := s.Delete(ctx, app, "surfer-abc123"); err != nil {
		t.Fatalf("delete claimed: %v", err)
	}
	if _, err := s.Get(ctx, app, "surfer-abc123"); !errors.Is(err, ErrNotFound) {
		t.Errorf("claimed id survives delete: %v", err)
	}
	if _, err := s.Create(ctx, app, "WaveLord", "surfer-abc123"); err != nil {
		t.Fatalf("re-claim after delete: %v", err)
	}

	// Exactly one concurrent claimant can win a member id, and the losers'
	// nicknames must not be leaked into the claim table.
	const memClaimants = 8
	memErrs := make(chan error, memClaimants)
	for i := 0; i < memClaimants; i++ {
		nick := fmt.Sprintf("Wave-%d", i)
		go func() {
			_, err := s.Create(ctx, app, nick, "surfer-contested")
			memErrs <- err
		}()
	}
	memWins := 0
	for i := 0; i < memClaimants; i++ {
		if err := <-memErrs; err == nil {
			memWins++
		} else if !errors.Is(err, ErrMemberTaken) {
			t.Errorf("concurrent member claim: %v", err)
		}
	}
	if memWins != 1 {
		t.Errorf("concurrent member claim: %d wins, want exactly 1", memWins)
	}
	memWinner, err := s.Get(ctx, app, "surfer-contested")
	if err != nil {
		t.Fatal(err)
	}
	memClaims := 0
	for i := 0; i < memClaimants; i++ {
		nick := fmt.Sprintf("Wave-%d", i)
		got, err := s.GetByNickname(ctx, app, nick)
		switch {
		case err == nil:
			memClaims++
			if got.ID != "surfer-contested" || !strings.EqualFold(memWinner.Nickname, nick) {
				t.Errorf("claim %q inconsistent: maps to %s, record nick %q", nick, got.ID, memWinner.Nickname)
			}
		case !errors.Is(err, ErrNotFound):
			t.Errorf("GetByNickname(%q): %v", nick, err)
		}
	}
	if memClaims != 1 {
		t.Errorf("claimed member holds %d nickname claims, want exactly 1", memClaims)
	}
}

func TestMemStore(t *testing.T) { testStore(t, NewMemStore(), "app_memtest") }

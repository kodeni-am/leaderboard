package users

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// testStore is the conformance suite every Store implementation must pass.
// app must be unique per run for stores with persistent backends.
func testStore(t *testing.T, s Store, app string) {
	ctx := context.Background()

	// Create trims whitespace, mints a plr_ id, and stores the display form.
	u, err := s.Create(ctx, app, "  Ninja  ")
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
	if _, err := s.Create(ctx, app, "ninja"); !errors.Is(err, ErrNicknameTaken) {
		t.Errorf("case-insensitive dup: got %v, want ErrNicknameTaken", err)
	}
	if _, err := s.Create(ctx, app+"other", "Ninja"); err != nil {
		t.Errorf("same nick in another app: %v", err)
	}

	// Invalid nicknames are rejected before any state changes.
	for _, bad := range []string{"", "   ", strings.Repeat("x", 33), "a\x00b", "line\nbreak"} {
		if _, err := s.Create(ctx, app, bad); !errors.Is(err, ErrInvalidNickname) {
			t.Errorf("Create(%q): got %v, want ErrInvalidNickname", bad, err)
		}
	}
	// 32 runes of multibyte characters are valid (rune count, not bytes).
	if _, err := s.Create(ctx, app, strings.Repeat("ü", 32)); err != nil {
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
	u2, err := s.Create(ctx, app, "Pixel")
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
	if _, err := s.Create(ctx, app, "Pixel"); err != nil {
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
			_, err := s.Create(ctx, app, "Contested")
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
}

func TestMemStore(t *testing.T) { testStore(t, NewMemStore(), "app_memtest") }

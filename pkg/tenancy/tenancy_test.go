package tenancy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/kodeni-am/leaderboard/pkg/engine"
	"github.com/redis/go-redis/v9"
)

// storeContract exercises a Store implementation end to end.
func storeContract(t *testing.T, s Store) {
	ctx := context.Background()
	// Unique owner per run so the shared Redis store doesn't accumulate apps
	// across test runs.
	owner := "usr_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	app, key, err := s.CreateApp(ctx, owner, "Pong")
	if err != nil {
		t.Fatal(err)
	}
	if app.ID == "" || key == "" || app.OwnerUserID != owner {
		t.Fatalf("unexpected app: %+v key=%q", app, key)
	}
	// Owner scoping.
	owned, err := s.ListApps(ctx, owner)
	if err != nil || len(owned) != 1 || owned[0].ID != app.ID {
		t.Fatalf("ListApps(owner): %v / %v", owned, err)
	}
	if other, _ := s.ListApps(ctx, owner+"_nobody"); len(other) != 0 {
		t.Errorf("ListApps(other) should be empty, got %v", other)
	}
	// Auth with the plaintext key resolves the app; a wrong key fails.
	got, err := s.AppByKey(ctx, key)
	if err != nil || got.ID != app.ID {
		t.Fatalf("AppByKey: %v / %v", got, err)
	}
	if _, err := s.AppByKey(ctx, "lb_wrong"); !errors.Is(err, ErrInvalidKey) {
		t.Errorf("expected ErrInvalidKey, got %v", err)
	}
	// Board definitions persist and round-trip.
	lb := engine.LogicalBoard{
		App:     app.ID,
		Board:   "high",
		Config:  engine.BoardConfig{SortOrder: engine.SortDesc, UpdatePolicy: engine.UpdateBest},
		Windows: []engine.WindowSpec{{Kind: engine.WindowAllTime}, {Kind: engine.WindowDaily}},
	}
	if err := s.UpsertBoard(ctx, lb); err != nil {
		t.Fatal(err)
	}
	back, err := s.GetBoard(ctx, app.ID, "high")
	if err != nil {
		t.Fatal(err)
	}
	if back.Board != "high" || len(back.Windows) != 2 || back.Config.SortOrder != engine.SortDesc {
		t.Errorf("board round-trip mismatch: %+v", back)
	}
	if _, err := s.GetBoard(ctx, app.ID, "ghost"); !errors.Is(err, ErrBoardNotFound) {
		t.Errorf("expected ErrBoardNotFound, got %v", err)
	}
	list, _ := s.ListBoards(ctx, app.ID)
	if len(list) != 1 {
		t.Errorf("ListBoards = %d, want 1", len(list))
	}
	all, _ := s.AllBoards(ctx)
	if len(all) < 1 {
		t.Errorf("AllBoards empty")
	}
	// Board for unknown app is rejected.
	if err := s.UpsertBoard(ctx, engine.LogicalBoard{App: "app_nope", Board: "x"}); !errors.Is(err, ErrAppNotFound) {
		t.Errorf("expected ErrAppNotFound, got %v", err)
	}
}

func TestMemStoreContract(t *testing.T) {
	storeContract(t, NewMemStore())
}

func TestRedisStoreContract(t *testing.T) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	rdb := redis.NewUniversalClient(&redis.UniversalOptions{Addrs: []string{addr}})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("redis not available: %v", err)
	}
	// Isolate: flush is too broad; this test uses unique random ids so it is safe.
	storeContract(t, NewRedisStore(rdb))
}

func TestAuthMiddleware(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	_, key, _ := s.CreateApp(ctx, "usr_game", "Game")

	var sawApp App
	handler := Authenticate(s)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		app, ok := AppFromContext(r.Context())
		if !ok {
			t.Error("no app in context")
		}
		sawApp = app
		w.WriteHeader(http.StatusOK)
	}))

	// Missing key -> 401.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("missing key: got %d, want 401", rec.Code)
	}

	// Valid Bearer key -> 200 and app in context.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || sawApp.Name != "Game" {
		t.Errorf("valid key: code=%d app=%v", rec.Code, sawApp)
	}

	// X-API-Key header also works.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-API-Key", key)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("X-API-Key: got %d, want 200", rec.Code)
	}
}

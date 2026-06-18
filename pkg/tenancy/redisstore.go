package tenancy

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kodeni-am/leaderboard/pkg/engine"
	"github.com/redis/go-redis/v9"
)

// RedisStore persists tenants and board definitions in Redis. For a hosted
// deployment this is typically MemoryDB (durable) or a small DynamoDB-backed
// store; the interface is identical.
type RedisStore struct {
	rdb redis.UniversalClient
}

func NewRedisStore(rdb redis.UniversalClient) *RedisStore { return &RedisStore{rdb: rdb} }

func appKey(id string) string        { return "ten:app:" + id }
func apiKeyKey(hash string) string   { return "ten:key:" + hash }
func boardKey(app, b string) string  { return "ten:board:" + app + ":" + b }
func boardSetKey(app string) string  { return "ten:boards:" + app }
func appSetKey() string              { return "ten:apps" }
func ownerAppsKey(uid string) string { return "ten:owner:" + uid }

func (s *RedisStore) CreateApp(ctx context.Context, ownerUserID, name string) (App, string, error) {
	id, err := newID("app_")
	if err != nil {
		return App{}, "", err
	}
	plain, hash, err := newAPIKey()
	if err != nil {
		return App{}, "", err
	}
	app := App{ID: id, Name: name, OwnerUserID: ownerUserID, CreatedAt: time.Now().UTC()}
	data, _ := json.Marshal(app)
	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, appKey(id), data, 0)
	pipe.Set(ctx, apiKeyKey(hash), id, 0)
	pipe.SAdd(ctx, appSetKey(), id)
	if ownerUserID != "" {
		pipe.SAdd(ctx, ownerAppsKey(ownerUserID), id)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return App{}, "", err
	}
	return app, plain, nil
}

func (s *RedisStore) ListApps(ctx context.Context, ownerUserID string) ([]App, error) {
	ids, err := s.rdb.SMembers(ctx, ownerAppsKey(ownerUserID)).Result()
	if err != nil {
		return nil, err
	}
	out := make([]App, 0, len(ids))
	for _, id := range ids {
		app, err := s.GetApp(ctx, id)
		if err == ErrAppNotFound {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, app)
	}
	return out, nil
}

func (s *RedisStore) GetApp(ctx context.Context, id string) (App, error) {
	data, err := s.rdb.Get(ctx, appKey(id)).Bytes()
	if err == redis.Nil {
		return App{}, ErrAppNotFound
	}
	if err != nil {
		return App{}, err
	}
	var app App
	if err := json.Unmarshal(data, &app); err != nil {
		return App{}, err
	}
	return app, nil
}

func (s *RedisStore) AppByKey(ctx context.Context, plaintextKey string) (App, error) {
	id, err := s.rdb.Get(ctx, apiKeyKey(hashKey(plaintextKey))).Result()
	if err == redis.Nil {
		return App{}, ErrInvalidKey
	}
	if err != nil {
		return App{}, err
	}
	return s.GetApp(ctx, id)
}

func (s *RedisStore) UpsertBoard(ctx context.Context, lb engine.LogicalBoard) error {
	if _, err := s.GetApp(ctx, lb.App); err != nil {
		return err
	}
	data, err := json.Marshal(lb)
	if err != nil {
		return err
	}
	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, boardKey(lb.App, lb.Board), data, 0)
	pipe.SAdd(ctx, boardSetKey(lb.App), lb.Board)
	_, err = pipe.Exec(ctx)
	return err
}

func (s *RedisStore) GetBoard(ctx context.Context, app, board string) (engine.LogicalBoard, error) {
	data, err := s.rdb.Get(ctx, boardKey(app, board)).Bytes()
	if err == redis.Nil {
		return engine.LogicalBoard{}, ErrBoardNotFound
	}
	if err != nil {
		return engine.LogicalBoard{}, err
	}
	var lb engine.LogicalBoard
	if err := json.Unmarshal(data, &lb); err != nil {
		return engine.LogicalBoard{}, err
	}
	return lb, nil
}

func (s *RedisStore) ListBoards(ctx context.Context, app string) ([]engine.LogicalBoard, error) {
	ids, err := s.rdb.SMembers(ctx, boardSetKey(app)).Result()
	if err != nil {
		return nil, err
	}
	out := make([]engine.LogicalBoard, 0, len(ids))
	for _, b := range ids {
		lb, err := s.GetBoard(ctx, app, b)
		if err == ErrBoardNotFound {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, lb)
	}
	return out, nil
}

func (s *RedisStore) AllBoards(ctx context.Context) ([]engine.LogicalBoard, error) {
	appIDs, err := s.rdb.SMembers(ctx, appSetKey()).Result()
	if err != nil {
		return nil, err
	}
	var out []engine.LogicalBoard
	for _, id := range appIDs {
		boards, err := s.ListBoards(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("listboards %s: %w", id, err)
		}
		out = append(out, boards...)
	}
	return out, nil
}

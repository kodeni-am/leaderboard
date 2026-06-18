package tenancy

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kodeni-am/leaderboard/pkg/engine"
	"github.com/redis/go-redis/v9"
)

// RedisStore persists tenants, API keys, and board definitions in Redis. For a
// hosted deployment this is typically MemoryDB (durable) or a small
// DynamoDB-backed store; the interface is identical.
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
func keyMetaKey(keyID string) string { return "ten:keymeta:" + keyID }
func keyHashKey(keyID string) string { return "ten:keyhash:" + keyID }
func appKeysKey(appID string) string { return "ten:appkeys:" + appID }

func (s *RedisStore) CreateApp(ctx context.Context, ownerUserID, name string) (App, string, error) {
	id, err := newID("app_")
	if err != nil {
		return App{}, "", err
	}
	app := App{ID: id, Name: name, OwnerUserID: ownerUserID, CreatedAt: time.Now().UTC()}
	data, _ := json.Marshal(app)
	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, appKey(id), data, 0)
	pipe.SAdd(ctx, appSetKey(), id)
	if ownerUserID != "" {
		pipe.SAdd(ctx, ownerAppsKey(ownerUserID), id)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return App{}, "", err
	}
	plain, _, err := s.IssueKey(ctx, id)
	if err != nil {
		return App{}, "", err
	}
	return app, plain, nil
}

func (s *RedisStore) IssueKey(ctx context.Context, appID string) (string, APIKey, error) {
	if _, err := s.GetApp(ctx, appID); err != nil {
		return "", APIKey{}, err
	}
	plain, hash, err := newAPIKey()
	if err != nil {
		return "", APIKey{}, err
	}
	keyID, err := newID("key_")
	if err != nil {
		return "", APIKey{}, err
	}
	k := APIKey{ID: keyID, AppID: appID, Prefix: keyPrefix(plain), CreatedAt: time.Now().UTC()}
	meta, _ := json.Marshal(k)
	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, apiKeyKey(hash), appID, 0)
	pipe.Set(ctx, keyMetaKey(keyID), meta, 0)
	pipe.Set(ctx, keyHashKey(keyID), hash, 0)
	pipe.SAdd(ctx, appKeysKey(appID), keyID)
	if _, err := pipe.Exec(ctx); err != nil {
		return "", APIKey{}, err
	}
	return plain, k, nil
}

func (s *RedisStore) ListKeys(ctx context.Context, appID string) ([]APIKey, error) {
	ids, err := s.rdb.SMembers(ctx, appKeysKey(appID)).Result()
	if err != nil {
		return nil, err
	}
	out := make([]APIKey, 0, len(ids))
	for _, kid := range ids {
		data, err := s.rdb.Get(ctx, keyMetaKey(kid)).Bytes()
		if err == redis.Nil {
			continue
		}
		if err != nil {
			return nil, err
		}
		var k APIKey
		if err := json.Unmarshal(data, &k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, nil
}

func (s *RedisStore) RevokeKey(ctx context.Context, appID, keyID string) error {
	data, err := s.rdb.Get(ctx, keyMetaKey(keyID)).Bytes()
	if err == redis.Nil {
		return ErrKeyNotFound
	}
	if err != nil {
		return err
	}
	var k APIKey
	if err := json.Unmarshal(data, &k); err != nil {
		return err
	}
	if k.AppID != appID {
		return ErrKeyNotFound
	}
	hash, err := s.rdb.Get(ctx, keyHashKey(keyID)).Result()
	if err != nil && err != redis.Nil {
		return err
	}
	pipe := s.rdb.TxPipeline()
	if hash != "" {
		pipe.Del(ctx, apiKeyKey(hash))
	}
	pipe.Del(ctx, keyMetaKey(keyID))
	pipe.Del(ctx, keyHashKey(keyID))
	pipe.SRem(ctx, appKeysKey(appID), keyID)
	_, err = pipe.Exec(ctx)
	return err
}

func (s *RedisStore) DeleteApp(ctx context.Context, appID string) error {
	app, err := s.GetApp(ctx, appID)
	if err != nil {
		return err
	}
	keyIDs, err := s.rdb.SMembers(ctx, appKeysKey(appID)).Result()
	if err != nil {
		return err
	}
	for _, kid := range keyIDs {
		_ = s.RevokeKey(ctx, appID, kid)
	}
	boardIDs, err := s.rdb.SMembers(ctx, boardSetKey(appID)).Result()
	if err != nil {
		return err
	}
	pipe := s.rdb.TxPipeline()
	for _, b := range boardIDs {
		pipe.Del(ctx, boardKey(appID, b))
	}
	pipe.Del(ctx, boardSetKey(appID))
	pipe.Del(ctx, appKeysKey(appID))
	pipe.Del(ctx, appKey(appID))
	pipe.SRem(ctx, appSetKey(), appID)
	if app.OwnerUserID != "" {
		pipe.SRem(ctx, ownerAppsKey(app.OwnerUserID), appID)
	}
	_, err = pipe.Exec(ctx)
	return err
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

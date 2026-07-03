package users

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisStore implements Store on Redis. All of an app's keys share a {app}
// hash tag (one cluster slot), which lets create/rename run as atomic Lua
// scripts — the invariant "no two players share a lowercased nickname" holds
// under concurrent claims.
type RedisStore struct {
	rdb redis.UniversalClient
}

func NewRedisStore(rdb redis.UniversalClient) *RedisStore { return &RedisStore{rdb: rdb} }

func playerKey(app, id string) string { return "plr:{" + app + "}:user:" + id }
func namesKey(app string) string      { return "plr:{" + app + "}:names" }
func nickKey(app string) string       { return "plr:{" + app + "}:nick" }

// createScript claims the lowercased nickname and writes the user record and
// the id->display mapping in one atomic step. Returns 0 if the name is taken.
// KEYS: 1=nick hash, 2=names hash, 3=user record
// ARGV: 1=lower nick, 2=id, 3=display nick, 4=user JSON
var createScript = redis.NewScript(`
if redis.call('HSETNX', KEYS[1], ARGV[1], ARGV[2]) == 0 then return 0 end
redis.call('HSET', KEYS[2], ARGV[2], ARGV[3])
redis.call('SET', KEYS[3], ARGV[4])
return 1
`)

// renameScript claims the new lowercased nickname, releases the old one (only
// if this player still owns it), and updates the record + display mapping. A
// case-only rename (same lower key) skips the claim and just updates display.
// KEYS: 1=nick hash, 2=names hash, 3=user record
// ARGV: 1=new lower, 2=old lower, 3=id, 4=new display, 5=user JSON
var renameScript = redis.NewScript(`
if ARGV[1] ~= ARGV[2] then
  if redis.call('HSETNX', KEYS[1], ARGV[1], ARGV[3]) == 0 then return 0 end
  if redis.call('HGET', KEYS[1], ARGV[2]) == ARGV[3] then
    redis.call('HDEL', KEYS[1], ARGV[2])
  end
end
redis.call('HSET', KEYS[2], ARGV[3], ARGV[4])
redis.call('SET', KEYS[3], ARGV[5])
return 1
`)

func (s *RedisStore) Create(ctx context.Context, appID, nickname string) (User, error) {
	display, lower, err := normalizeNickname(nickname)
	if err != nil {
		return User{}, err
	}
	id, err := newID()
	if err != nil {
		return User{}, err
	}
	now := time.Now().UTC()
	u := User{ID: id, Nickname: display, CreatedAt: now, UpdatedAt: now}
	data, err := json.Marshal(u)
	if err != nil {
		return User{}, err
	}
	ok, err := createScript.Run(ctx, s.rdb,
		[]string{nickKey(appID), namesKey(appID), playerKey(appID, id)},
		lower, id, display, data).Int()
	if err != nil {
		return User{}, err
	}
	if ok == 0 {
		return User{}, ErrNicknameTaken
	}
	return u, nil
}

func (s *RedisStore) Get(ctx context.Context, appID, id string) (User, error) {
	data, err := s.rdb.Get(ctx, playerKey(appID, id)).Bytes()
	if err == redis.Nil {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	var u User
	if err := json.Unmarshal(data, &u); err != nil {
		return User{}, err
	}
	return u, nil
}

func (s *RedisStore) GetByNickname(ctx context.Context, appID, nickname string) (User, error) {
	_, lower, err := normalizeNickname(nickname)
	if err != nil {
		return User{}, err
	}
	id, err := s.rdb.HGet(ctx, nickKey(appID), lower).Result()
	if err == redis.Nil {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return s.Get(ctx, appID, id)
}

func (s *RedisStore) Rename(ctx context.Context, appID, id, nickname string) (User, error) {
	display, lower, err := normalizeNickname(nickname)
	if err != nil {
		return User{}, err
	}
	u, err := s.Get(ctx, appID, id)
	if err != nil {
		return User{}, err
	}
	_, oldLower, err := normalizeNickname(u.Nickname)
	if err != nil {
		return User{}, err
	}
	u.Nickname = display
	u.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(u)
	if err != nil {
		return User{}, err
	}
	ok, err := renameScript.Run(ctx, s.rdb,
		[]string{nickKey(appID), namesKey(appID), playerKey(appID, id)},
		lower, oldLower, id, display, data).Int()
	if err != nil {
		return User{}, err
	}
	if ok == 0 {
		return User{}, ErrNicknameTaken
	}
	return u, nil
}

func (s *RedisStore) Nicknames(ctx context.Context, appID string, ids []string) (map[string]string, error) {
	if len(ids) == 0 {
		return map[string]string{}, nil
	}
	vals, err := s.rdb.HMGet(ctx, namesKey(appID), ids...).Result()
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(ids))
	for i, v := range vals {
		if name, ok := v.(string); ok {
			out[ids[i]] = name
		}
	}
	return out, nil
}

package accounts

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisStores implements UserStore, SessionStore, and TokenStore on Redis.
type RedisStores struct {
	rdb redis.UniversalClient
}

func NewRedisStores(rdb redis.UniversalClient) *RedisStores { return &RedisStores{rdb: rdb} }

func userKey(id string) string          { return "acct:user:" + id }
func emailKey(email string) string      { return "acct:email:" + email }
func sessKey(tok string) string         { return "acct:sess:" + tok }
func userSessKey(uid string) string     { return "acct:usess:" + uid }
func tokKey(purpose, tok string) string { return "acct:tok:" + purpose + ":" + tok }

func (s *RedisStores) CreateUser(ctx context.Context, u User) error {
	ok, err := s.rdb.SetNX(ctx, emailKey(u.Email), u.ID, 0).Result()
	if err != nil {
		return err
	}
	if !ok {
		return ErrEmailTaken
	}
	data, err := json.Marshal(u)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, userKey(u.ID), data, 0).Err()
}

func (s *RedisStores) GetByEmail(ctx context.Context, email string) (User, error) {
	id, err := s.rdb.Get(ctx, emailKey(email)).Result()
	if err == redis.Nil {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, err
	}
	return s.GetByID(ctx, id)
}

func (s *RedisStores) GetByID(ctx context.Context, id string) (User, error) {
	data, err := s.rdb.Get(ctx, userKey(id)).Bytes()
	if err == redis.Nil {
		return User{}, ErrUserNotFound
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

func (s *RedisStores) Update(ctx context.Context, u User) error {
	data, err := json.Marshal(u)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, userKey(u.ID), data, 0).Err()
}

func (s *RedisStores) Create(ctx context.Context, userID string, ttl time.Duration) (string, error) {
	tok, err := newToken()
	if err != nil {
		return "", err
	}
	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, sessKey(tok), userID, ttl)
	pipe.SAdd(ctx, userSessKey(userID), tok)
	pipe.Expire(ctx, userSessKey(userID), ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return "", err
	}
	return tok, nil
}

func (s *RedisStores) UserID(ctx context.Context, token string) (string, error) {
	uid, err := s.rdb.Get(ctx, sessKey(token)).Result()
	if err == redis.Nil {
		return "", ErrNoSession
	}
	if err != nil {
		return "", err
	}
	return uid, nil
}

func (s *RedisStores) Delete(ctx context.Context, token string) error {
	uid, err := s.rdb.Get(ctx, sessKey(token)).Result()
	if err != nil && err != redis.Nil {
		return err
	}
	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, sessKey(token))
	if uid != "" {
		pipe.SRem(ctx, userSessKey(uid), token)
	}
	_, err = pipe.Exec(ctx)
	return err
}

func (s *RedisStores) DeleteAllForUser(ctx context.Context, userID string) error {
	toks, err := s.rdb.SMembers(ctx, userSessKey(userID)).Result()
	if err != nil {
		return err
	}
	pipe := s.rdb.TxPipeline()
	for _, t := range toks {
		pipe.Del(ctx, sessKey(t))
	}
	pipe.Del(ctx, userSessKey(userID))
	_, err = pipe.Exec(ctx)
	return err
}

func (s *RedisStores) Issue(ctx context.Context, purpose, userID string, ttl time.Duration) (string, error) {
	tok, err := newToken()
	if err != nil {
		return "", err
	}
	if err := s.rdb.Set(ctx, tokKey(purpose, tok), userID, ttl).Err(); err != nil {
		return "", err
	}
	return tok, nil
}

func (s *RedisStores) Consume(ctx context.Context, purpose, token string) (string, error) {
	uid, err := s.rdb.GetDel(ctx, tokKey(purpose, token)).Result()
	if err == redis.Nil {
		return "", ErrBadToken
	}
	if err != nil {
		return "", err
	}
	return uid, nil
}

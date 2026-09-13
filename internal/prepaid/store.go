package prepaid

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	trafficStatePrefix = "prepaid:traffic:user:"
	trafficBlockedSet  = "prepaid:traffic:blocked"

	PhaseBlocking = "blocking"
	PhaseBlocked  = "blocked"
)

type TrafficState struct {
	UserID              int64     `json:"userId"`
	Phase               string    `json:"phase"`
	ExhaustedLimitBytes float64   `json:"exhaustedLimitBytes"`
	BlockedAt           time.Time `json:"blockedAt"`
}

type Store struct {
	client *redis.Client
}

func NewStore(redisURL string) (*Store, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse REDIS_URL: %w", err)
	}
	return &Store{client: redis.NewClient(opts)}, nil
}

func (s *Store) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

func (s *Store) Close() error {
	return s.client.Close()
}

func (s *Store) GetTrafficState(ctx context.Context, userID int64) (*TrafficState, error) {
	data, err := s.client.Get(ctx, trafficStateKey(userID)).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get traffic state for user %d: %w", userID, err)
	}

	var state TrafficState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode traffic state for user %d: %w", userID, err)
	}
	return &state, nil
}

func (s *Store) SaveTrafficState(ctx context.Context, state TrafficState) error {
	if state.UserID <= 0 {
		return fmt.Errorf("save traffic state: invalid user id %d", state.UserID)
	}
	if state.Phase != PhaseBlocking && state.Phase != PhaseBlocked {
		return fmt.Errorf("save traffic state: invalid phase %q", state.Phase)
	}

	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode traffic state: %w", err)
	}

	pipe := s.client.TxPipeline()
	pipe.Set(ctx, trafficStateKey(state.UserID), data, 0)
	pipe.SAdd(ctx, trafficBlockedSet, strconv.FormatInt(state.UserID, 10))
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("save traffic state for user %d: %w", state.UserID, err)
	}
	return nil
}

func (s *Store) DeleteTrafficState(ctx context.Context, userID int64) error {
	member := strconv.FormatInt(userID, 10)
	pipe := s.client.TxPipeline()
	pipe.Del(ctx, trafficStateKey(userID))
	pipe.SRem(ctx, trafficBlockedSet, member)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("delete traffic state for user %d: %w", userID, err)
	}
	return nil
}

func (s *Store) ListTrafficStateUserIDs(ctx context.Context) ([]int64, error) {
	members, err := s.client.SMembers(ctx, trafficBlockedSet).Result()
	if err != nil {
		return nil, fmt.Errorf("list blocked traffic users: %w", err)
	}

	ids := make([]int64, 0, len(members))
	for _, member := range members {
		id, err := strconv.ParseInt(member, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid user id %q in %s: %w", member, trafficBlockedSet, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func trafficStateKey(userID int64) string {
	return trafficStatePrefix + strconv.FormatInt(userID, 10)
}

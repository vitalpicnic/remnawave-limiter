package prepaid

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/remnawave/limiter/internal/api"
	"github.com/sirupsen/logrus"
)

type Panel interface {
	GetTrafficUserByID(ctx context.Context, userID int64) (*api.TrafficUserData, error)
	UpdateTrafficUser(ctx context.Context, req api.UpdateTrafficUserRequest) error
	ListLimitedTrafficUsers(ctx context.Context) ([]api.TrafficUserData, error)
}

type StateStore interface {
	GetTrafficState(ctx context.Context, userID int64) (*TrafficState, error)
	SaveTrafficState(ctx context.Context, state TrafficState) error
	DeleteTrafficState(ctx context.Context, userID int64) error
	ListTrafficStateUserIDs(ctx context.Context) ([]int64, error)
}

type Service struct {
	panel          Panel
	store          StateStore
	limitedSquad   string
	unlimitedSquad string
	logger         *logrus.Logger
	locks          sync.Map
}

func NewService(panel Panel, store StateStore, limitedSquad, unlimitedSquad string, logger *logrus.Logger) *Service {
	return &Service{
		panel:          panel,
		store:          store,
		limitedSquad:   limitedSquad,
		unlimitedSquad: unlimitedSquad,
		logger:         logger,
	}
}

func (s *Service) HandleEvent(ctx context.Context, event string, userID int64) error {
	if userID <= 0 {
		return fmt.Errorf("invalid user id %d", userID)
	}

	unlock := s.lockUser(userID)
	defer unlock()

	switch event {
	case "user.limited":
		return s.handleLimited(ctx, userID)
	case "user.modified", "user.traffic_reset":
		return s.reconcileUser(ctx, userID)
	default:
		return nil
	}
}

func (s *Service) Reconcile(ctx context.Context) error {
	var firstErr error

	// First, discover users that are currently LIMITED in Remnawave.
	// This recovers from a lost user.limited webhook.
	limitedUsers, err := s.panel.ListLimitedTrafficUsers(ctx)
	if err != nil {
		firstErr = err
		s.logger.WithError(err).Warn("Не удалось получить список LIMITED пользователей")
	} else {
		for i := range limitedUsers {
			user := limitedUsers[i]
			unlock := s.lockUser(user.ID)
			if err := s.reconcileLimitedUser(ctx, &user); err != nil {
				s.logger.WithError(err).WithField("user_id", user.ID).Error("Ошибка reconcile LIMITED пользователя")
				if firstErr == nil {
					firstErr = err
				}
			}
			unlock()
		}
	}

	// Then, revisit all users for which the prepaid limiter already owns state.
	ids, err := s.store.ListTrafficStateUserIDs(ctx)
	if err != nil {
		if firstErr == nil {
			firstErr = err
		}
		s.logger.WithError(err).Warn("Не удалось получить список prepaid-состояний из Redis")
		return firstErr
	}

	for _, userID := range ids {
		unlock := s.lockUser(userID)
		if err := s.reconcileUser(ctx, userID); err != nil {
			s.logger.WithError(err).WithField("user_id", userID).Error("Ошибка reconcile prepaid пользователя")
			if firstErr == nil {
				firstErr = err
			}
		}
		unlock()
	}

	return firstErr
}

func (s *Service) RunReconciler(ctx context.Context, interval time.Duration) {
	run := func() {
		if err := s.Reconcile(ctx); err != nil && ctx.Err() == nil {
			s.logger.WithError(err).Warn("Reconcile завершился с ошибкой")
		}
	}

	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func (s *Service) handleLimited(ctx context.Context, userID int64) error {
	state, err := s.store.GetTrafficState(ctx, userID)
	if err != nil {
		return err
	}
	if state != nil {
		// The user is already controlled by us. Do not overwrite a newly
		// assigned prepaid limit if the admin set it before reset-traffic.
		return s.reconcileState(ctx, state)
	}

	user, err := s.panel.GetTrafficUserByID(ctx, userID)
	if err != nil {
		return err
	}
	return s.startBlocking(ctx, user)
}

func (s *Service) reconcileLimitedUser(ctx context.Context, user *api.TrafficUserData) error {
	state, err := s.store.GetTrafficState(ctx, user.ID)
	if err != nil {
		return err
	}
	if state != nil {
		return s.reconcileStateWithUser(ctx, state, user)
	}
	return s.startBlocking(ctx, user)
}

func (s *Service) startBlocking(ctx context.Context, user *api.TrafficUserData) error {
	if user.Status != "LIMITED" {
		return nil
	}
	if !hasSquad(user.ActiveInternalSquads, s.limitedSquad) {
		// A LIMITED user outside our paid squad is not managed by this module.
		return nil
	}
	if user.TrafficLimitBytes <= 0 {
		s.logger.WithField("user_id", user.ID).Warn("LIMITED пользователь имеет trafficLimitBytes <= 0; пропускаю")
		return nil
	}

	state := TrafficState{
		UserID:              user.ID,
		Phase:               PhaseBlocking,
		ExhaustedLimitBytes: user.TrafficLimitBytes,
		BlockedAt:           time.Now().UTC(),
	}
	if err := s.store.SaveTrafficState(ctx, state); err != nil {
		return err
	}

	return s.finishBlocking(ctx, &state, user)
}

func (s *Service) reconcileUser(ctx context.Context, userID int64) error {
	state, err := s.store.GetTrafficState(ctx, userID)
	if err != nil {
		return err
	}
	if state == nil {
		return nil
	}
	return s.reconcileState(ctx, state)
}

func (s *Service) reconcileState(ctx context.Context, state *TrafficState) error {
	user, err := s.panel.GetTrafficUserByID(ctx, state.UserID)
	if err != nil {
		return err
	}
	return s.reconcileStateWithUser(ctx, state, user)
}

func (s *Service) reconcileStateWithUser(ctx context.Context, state *TrafficState, user *api.TrafficUserData) error {
	switch state.Phase {
	case PhaseBlocking:
		return s.finishBlocking(ctx, state, user)
	case PhaseBlocked:
		return s.reconcileBlocked(ctx, state, user)
	default:
		return fmt.Errorf("user %d has unknown prepaid phase %q", state.UserID, state.Phase)
	}
}

func (s *Service) finishBlocking(ctx context.Context, state *TrafficState, user *api.TrafficUserData) error {
	// Do not reactivate an account that an administrator disabled or that expired.
	if user.Status == "DISABLED" || user.Status == "EXPIRED" {
		return nil
	}

	squads := squadUUIDs(user.ActiveInternalSquads)
	squads = removeString(squads, s.limitedSquad)
	squads = ensureString(squads, s.unlimitedSquad)

	zero := float64(0)
	active := "ACTIVE"
	noReset := "NO_RESET"

	if err := s.panel.UpdateTrafficUser(ctx, api.UpdateTrafficUserRequest{
		ID:                   user.ID,
		Status:               &active,
		TrafficLimitBytes:    &zero,
		TrafficLimitStrategy: &noReset,
		ActiveInternalSquads: squads,
	}); err != nil {
		return err
	}

	state.Phase = PhaseBlocked
	if err := s.store.SaveTrafficState(ctx, *state); err != nil {
		return err
	}

	s.logger.WithFields(logrus.Fields{
		"user_id":         user.ID,
		"username":        user.Username,
		"exhausted_bytes": state.ExhaustedLimitBytes,
	}).Info("Предоплаченный лимит исчерпан: LIMITED squad отключён, UNLIMITED оставлен")
	return nil
}

func (s *Service) reconcileBlocked(ctx context.Context, state *TrafficState, user *api.TrafficUserData) error {
	// Explicit admin disable/expiration always wins over the prepaid module.
	if user.Status == "DISABLED" || user.Status == "EXPIRED" {
		return nil
	}

	// New prepaid package is recognized only after the old counter was reset:
	// blocked + non-zero limit + used == 0.
	if user.TrafficLimitBytes > 0 {
		if user.UsedTrafficBytes == 0 {
			return s.activateNewPackage(ctx, state, user)
		}

		// A new limit may have been entered before Reset Traffic. Preserve it.
		// Reset Traffic will later make usedTrafficBytes == 0 and trigger activation.
		return nil
	}

	// trafficLimitBytes == 0 is the stable blocked state. Repair squad/status
	// if a previous API request or a manual edit left it inconsistent.
	squads := squadUUIDs(user.ActiveInternalSquads)
	desired := ensureString(removeString(squads, s.limitedSquad), s.unlimitedSquad)
	needsSquadRepair := !sameStringSet(squads, desired)
	needsStatusRepair := user.Status == "LIMITED"
	needsStrategyRepair := user.TrafficLimitStrategy != "NO_RESET"

	if !needsSquadRepair && !needsStatusRepair && !needsStrategyRepair {
		return nil
	}

	active := "ACTIVE"
	noReset := "NO_RESET"
	req := api.UpdateTrafficUserRequest{
		ID:                   user.ID,
		TrafficLimitStrategy: &noReset,
		ActiveInternalSquads: desired,
	}
	if needsStatusRepair {
		req.Status = &active
	}
	return s.panel.UpdateTrafficUser(ctx, req)
}

func (s *Service) activateNewPackage(ctx context.Context, _ *TrafficState, user *api.TrafficUserData) error {
	squads := squadUUIDs(user.ActiveInternalSquads)
	squads = ensureString(squads, s.unlimitedSquad)
	squads = ensureString(squads, s.limitedSquad)

	active := "ACTIVE"
	noReset := "NO_RESET"
	if err := s.panel.UpdateTrafficUser(ctx, api.UpdateTrafficUserRequest{
		ID:                   user.ID,
		Status:               &active,
		TrafficLimitStrategy: &noReset,
		ActiveInternalSquads: squads,
	}); err != nil {
		return err
	}

	if err := s.store.DeleteTrafficState(ctx, user.ID); err != nil {
		return err
	}

	s.logger.WithFields(logrus.Fields{
		"user_id":   user.ID,
		"username":  user.Username,
		"new_limit": user.TrafficLimitBytes,
	}).Info("Назначен новый предоплаченный пакет: LIMITED squad восстановлен")
	return nil
}

func (s *Service) lockUser(userID int64) func() {
	value, _ := s.locks.LoadOrStore(userID, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func hasSquad(squads []api.InternalSquad, uuid string) bool {
	for _, squad := range squads {
		if squad.UUID == uuid {
			return true
		}
	}
	return false
}

func squadUUIDs(squads []api.InternalSquad) []string {
	out := make([]string, 0, len(squads))
	for _, squad := range squads {
		if squad.UUID != "" {
			out = ensureString(out, squad.UUID)
		}
	}
	return out
}

func ensureString(items []string, value string) []string {
	for _, item := range items {
		if item == value {
			return items
		}
	}
	return append(items, value)
}

func removeString(items []string, value string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item != value {
			out = append(out, item)
		}
	}
	return out
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, v := range a {
		seen[v]++
	}
	for _, v := range b {
		seen[v]--
		if seen[v] < 0 {
			return false
		}
	}
	return true
}

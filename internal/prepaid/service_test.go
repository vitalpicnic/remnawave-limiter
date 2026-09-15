package prepaid

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/remnawave/limiter/internal/api"
	"github.com/sirupsen/logrus"
)

const (
	testLimitedSquad   = "11111111-1111-1111-1111-111111111111"
	testUnlimitedSquad = "22222222-2222-2222-2222-222222222222"
	testExtraSquad     = "33333333-3333-3333-3333-333333333333"
)

type fakePanel struct {
	users        map[int64]api.TrafficUserData
	limitedUsers []api.TrafficUserData
	updates      []api.UpdateTrafficUserRequest
}

func (f *fakePanel) GetTrafficUserByID(_ context.Context, userID int64) (*api.TrafficUserData, error) {
	u := f.users[userID]
	copyUser := u
	copyUser.ActiveInternalSquads = append([]api.InternalSquad(nil), u.ActiveInternalSquads...)
	return &copyUser, nil
}

func (f *fakePanel) UpdateTrafficUser(_ context.Context, req api.UpdateTrafficUserRequest) error {
	f.updates = append(f.updates, req)
	u := f.users[req.ID]
	if req.Status != nil {
		u.Status = *req.Status
	}
	if req.TrafficLimitBytes != nil {
		u.TrafficLimitBytes = *req.TrafficLimitBytes
	}
	if req.TrafficLimitStrategy != nil {
		u.TrafficLimitStrategy = *req.TrafficLimitStrategy
	}
	if req.ActiveInternalSquads != nil {
		u.ActiveInternalSquads = make([]api.InternalSquad, 0, len(req.ActiveInternalSquads))
		for _, id := range req.ActiveInternalSquads {
			u.ActiveInternalSquads = append(u.ActiveInternalSquads, api.InternalSquad{UUID: id})
		}
	}
	f.users[req.ID] = u
	return nil
}

func (f *fakePanel) ListLimitedTrafficUsers(_ context.Context) ([]api.TrafficUserData, error) {
	return append([]api.TrafficUserData(nil), f.limitedUsers...), nil
}

type fakeStore struct {
	states map[int64]TrafficState
}

func (f *fakeStore) GetTrafficState(_ context.Context, userID int64) (*TrafficState, error) {
	state, ok := f.states[userID]
	if !ok {
		return nil, nil
	}
	copyState := state
	return &copyState, nil
}

func (f *fakeStore) SaveTrafficState(_ context.Context, state TrafficState) error {
	f.states[state.UserID] = state
	return nil
}

func (f *fakeStore) DeleteTrafficState(_ context.Context, userID int64) error {
	delete(f.states, userID)
	return nil
}

func (f *fakeStore) ListTrafficStateUserIDs(_ context.Context) ([]int64, error) {
	ids := make([]int64, 0, len(f.states))
	for id := range f.states {
		ids = append(ids, id)
	}
	return ids, nil
}

func testService(panel *fakePanel, store *fakeStore) *Service {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	return NewService(panel, store, testLimitedSquad, testUnlimitedSquad, logger)
}

func TestLimitedEventCutsOnlyLimitedSquadAndSetsFallback(t *testing.T) {
	panel := &fakePanel{users: map[int64]api.TrafficUserData{
		1: {
			ID:                   1,
			Username:             "alice",
			Status:               "LIMITED",
			TrafficLimitBytes:    100,
			UsedTrafficBytes:     100,
			TrafficLimitStrategy: "NO_RESET",
			ActiveInternalSquads: []api.InternalSquad{
				{UUID: testLimitedSquad},
				{UUID: testExtraSquad},
			},
		},
	}}
	store := &fakeStore{states: map[int64]TrafficState{}}
	svc := testService(panel, store)

	if err := svc.HandleEvent(context.Background(), "user.limited", 1); err != nil {
		t.Fatal(err)
	}

	if len(panel.updates) != 1 {
		t.Fatalf("expected one update, got %d", len(panel.updates))
	}
	req := panel.updates[0]
	if req.Status == nil || *req.Status != "ACTIVE" {
		t.Fatalf("expected ACTIVE status, got %#v", req.Status)
	}
	if req.TrafficLimitBytes == nil || *req.TrafficLimitBytes != 0 {
		t.Fatalf("expected trafficLimitBytes=0, got %#v", req.TrafficLimitBytes)
	}
	if req.TrafficLimitStrategy == nil || *req.TrafficLimitStrategy != "NO_RESET" {
		t.Fatalf("expected NO_RESET, got %#v", req.TrafficLimitStrategy)
	}
	if containsString(req.ActiveInternalSquads, testLimitedSquad) {
		t.Fatal("limited squad must be removed")
	}
	if !containsString(req.ActiveInternalSquads, testUnlimitedSquad) {
		t.Fatal("unlimited squad must be present")
	}
	if !containsString(req.ActiveInternalSquads, testExtraSquad) {
		t.Fatal("unrelated squad must be preserved")
	}
	state := store.states[1]
	if state.Phase != PhaseBlocked || state.ExhaustedLimitBytes != 100 {
		t.Fatalf("unexpected state: %#v", state)
	}
}

func TestResetAloneDoesNotReactivateBlockedUser(t *testing.T) {
	panel := &fakePanel{users: map[int64]api.TrafficUserData{
		1: {
			ID:                   1,
			Status:               "ACTIVE",
			TrafficLimitBytes:    0,
			UsedTrafficBytes:     0,
			TrafficLimitStrategy: "NO_RESET",
			ActiveInternalSquads: []api.InternalSquad{{UUID: testUnlimitedSquad}},
		},
	}}
	store := &fakeStore{states: map[int64]TrafficState{
		1: {UserID: 1, Phase: PhaseBlocked, ExhaustedLimitBytes: 100},
	}}
	svc := testService(panel, store)

	if err := svc.HandleEvent(context.Background(), "user.traffic_reset", 1); err != nil {
		t.Fatal(err)
	}
	if len(panel.updates) != 0 {
		t.Fatalf("reset alone must not reactivate user; updates=%d", len(panel.updates))
	}
	if _, ok := store.states[1]; !ok {
		t.Fatal("blocked state must remain")
	}
}

func TestNewPaidLimitAfterResetReactivatesLimitedSquad(t *testing.T) {
	panel := &fakePanel{users: map[int64]api.TrafficUserData{
		1: {
			ID:                   1,
			Username:             "alice",
			Status:               "ACTIVE",
			TrafficLimitBytes:    500,
			UsedTrafficBytes:     0,
			TrafficLimitStrategy: "NO_RESET",
			ActiveInternalSquads: []api.InternalSquad{
				{UUID: testUnlimitedSquad},
				{UUID: testExtraSquad},
			},
		},
	}}
	store := &fakeStore{states: map[int64]TrafficState{
		1: {UserID: 1, Phase: PhaseBlocked, ExhaustedLimitBytes: 100},
	}}
	svc := testService(panel, store)

	if err := svc.HandleEvent(context.Background(), "user.modified", 1); err != nil {
		t.Fatal(err)
	}

	if len(panel.updates) != 1 {
		t.Fatalf("expected one activation update, got %d", len(panel.updates))
	}
	req := panel.updates[0]
	if req.TrafficLimitBytes != nil {
		t.Fatal("activation must preserve the admin-assigned new trafficLimitBytes")
	}
	if !containsString(req.ActiveInternalSquads, testLimitedSquad) {
		t.Fatal("limited squad must be restored")
	}
	if !containsString(req.ActiveInternalSquads, testUnlimitedSquad) {
		t.Fatal("unlimited squad must remain")
	}
	if !containsString(req.ActiveInternalSquads, testExtraSquad) {
		t.Fatal("unrelated squad must be preserved")
	}
	if _, ok := store.states[1]; ok {
		t.Fatal("blocked state must be cleared after activation")
	}
}

func TestNewLimitBeforeResetIsPreservedAndDoesNotReactivate(t *testing.T) {
	panel := &fakePanel{users: map[int64]api.TrafficUserData{
		1: {
			ID:                   1,
			Status:               "LIMITED",
			TrafficLimitBytes:    200,
			UsedTrafficBytes:     300,
			TrafficLimitStrategy: "NO_RESET",
			ActiveInternalSquads: []api.InternalSquad{{UUID: testUnlimitedSquad}},
		},
	}}
	store := &fakeStore{states: map[int64]TrafficState{
		1: {UserID: 1, Phase: PhaseBlocked, ExhaustedLimitBytes: 300},
	}}
	svc := testService(panel, store)

	if err := svc.HandleEvent(context.Background(), "user.modified", 1); err != nil {
		t.Fatal(err)
	}
	if len(panel.updates) != 0 {
		t.Fatalf("must wait for Reset Traffic, got %d updates", len(panel.updates))
	}
	if panel.users[1].TrafficLimitBytes != 200 {
		t.Fatalf("new paid limit must be preserved, got %v", panel.users[1].TrafficLimitBytes)
	}
}

func TestReconcileDiscoversMissedLimitedWebhook(t *testing.T) {
	user := api.TrafficUserData{
		ID:                   7,
		Username:             "bob",
		Status:               "LIMITED",
		TrafficLimitBytes:    100,
		UsedTrafficBytes:     100,
		TrafficLimitStrategy: "NO_RESET",
		ActiveInternalSquads: []api.InternalSquad{{UUID: testLimitedSquad}},
	}
	panel := &fakePanel{
		users:        map[int64]api.TrafficUserData{7: user},
		limitedUsers: []api.TrafficUserData{user},
	}
	store := &fakeStore{states: map[int64]TrafficState{}}
	svc := testService(panel, store)

	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.states[7]; !ok {
		t.Fatal("reconciler must create prepaid blocked state")
	}
	if panel.users[7].TrafficLimitBytes != 0 || panel.users[7].Status != "ACTIVE" {
		t.Fatalf("reconciler did not finish blocking: %#v", panel.users[7])
	}
}

func TestReconcileDiscoversV3LimitedUserThroughAPI(t *testing.T) {
	panel := &fakePanel{users: map[int64]api.TrafficUserData{
		66: {
			ID: 66, Status: "LIMITED", TrafficLimitBytes: 36700160,
			UsedTrafficBytes: 42362891, TrafficLimitStrategy: "NO_RESET",
			ActiveInternalSquads: []api.InternalSquad{
				{UUID: testLimitedSquad}, {UUID: testUnlimitedSquad}, {UUID: testExtraSquad},
			},
		},
	}}
	wireUser := func() map[string]interface{} {
		u := panel.users[66]
		return map[string]interface{}{
			"id": u.ID, "status": u.Status, "trafficLimitBytes": u.TrafficLimitBytes,
			"trafficLimitStrategy": u.TrafficLimitStrategy, "activeInternalSquads": u.ActiveInternalSquads,
			"userTraffic": map[string]interface{}{"usedTrafficBytes": u.UsedTrafficBytes},
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/users/stream":
			json.NewEncoder(w).Encode(map[string]interface{}{"response": map[string]interface{}{
				"users": []interface{}{wireUser()}, "nextCursor": nil, "hasMore": false,
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/users/66":
			json.NewEncoder(w).Encode(map[string]interface{}{"response": wireUser()})
		case r.Method == http.MethodPatch && r.URL.Path == "/api/users":
			var req api.UpdateTrafficUserRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if err := panel.UpdateTrafficUser(r.Context(), req); err != nil {
				t.Error(err)
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"response": wireUser()})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	store := &fakeStore{states: map[int64]TrafficState{}}
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	svc := NewService(api.NewClient(srv.URL, "test-token"), store, testLimitedSquad, testUnlimitedSquad, logger)
	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	u := panel.users[66]
	if u.Status != "ACTIVE" || u.TrafficLimitBytes != 0 {
		t.Errorf("unexpected user: %+v", u)
	}
	if hasSquad(u.ActiveInternalSquads, testLimitedSquad) || !hasSquad(u.ActiveInternalSquads, testUnlimitedSquad) || !hasSquad(u.ActiveInternalSquads, testExtraSquad) {
		t.Errorf("incorrect squads: %+v", u.ActiveInternalSquads)
	}
	if state := store.states[66]; state.Phase != PhaseBlocked || state.ExhaustedLimitBytes != 36700160 {
		t.Errorf("unexpected state: %+v", state)
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

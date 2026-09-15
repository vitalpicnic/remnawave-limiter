package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListLimitedTrafficUsersV3Pagination(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/users/stream" || r.URL.Query().Get("status") != "LIMITED" {
			t.Errorf("unexpected URL: %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		switch calls {
		case 1:
			fmt.Fprint(w, `{"response":{"users":[{"id":66,"status":"LIMITED","userTraffic":{"usedTrafficBytes":42362891}}],"nextCursor":66,"hasMore":true}}`)
		case 2:
			if r.URL.Query().Get("cursor") != "66" {
				t.Errorf("unexpected cursor: %s", r.URL)
			}
			fmt.Fprint(w, `{"response":{"users":[{"id":67,"status":"LIMITED"}],"nextCursor":null,"hasMore":false}}`)
		default:
			t.Errorf("unexpected extra page")
			fmt.Fprint(w, `{"response":{"users":[],"nextCursor":null}}`)
		}
	}))
	defer srv.Close()
	users, err := NewClient(srv.URL, "test-token").ListLimitedTrafficUsers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 {
		t.Fatalf("expected 2 users, got %d", len(users))
	}
	if users[0].ID != 66 || users[0].UsedTrafficBytes != 42362891 || users[1].ID != 67 {
		t.Errorf("unexpected users: %+v", users)
	}
}

func TestTrafficUserCounterFormats(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		want      float64
	}{
		{"legacy", `{"usedTrafficBytes":42}`, 42},
		{"nested", `{"userTraffic":{"usedTrafficBytes":42}}`, 42},
		{"nested zero wins", `{"usedTrafficBytes":42,"userTraffic":{"usedTrafficBytes":0}}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var user TrafficUserData
			if err := json.Unmarshal([]byte(tc.raw), &user); err != nil {
				t.Fatal(err)
			}
			if user.UsedTrafficBytes != tc.want {
				t.Errorf("got %v, want %v", user.UsedTrafficBytes, tc.want)
			}
		})
	}
}

func TestTrafficUsersStreamRejectsUnknownShape(t *testing.T) {
	var got trafficUsersStreamResponse
	if err := json.Unmarshal([]byte(`{"response":{"total":1}}`), &got); err == nil {
		t.Fatal("unknown response must not silently look like an empty list")
	}
}

func TestTrafficUsersStreamLegacyAndEmpty(t *testing.T) {
	for _, raw := range []string{
		`{"response":{"data":[],"nextCursor":null}}`,
		`{"response":{"users":[],"nextCursor":null}}`,
		`{"response":{"data":[{"id":66}],"nextCursor":null}}`,
	} {
		var got trafficUsersStreamResponse
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
}

func TestGetTrafficUserByIDNestedCounter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/users/66" {
			t.Errorf("unexpected URL: %s", r.URL)
		}
		fmt.Fprint(w, `{"response":{"id":66,"trafficLimitBytes":36700160,"userTraffic":{"usedTrafficBytes":42362891}}}`)
	}))
	defer srv.Close()
	user, err := NewClient(srv.URL, "test-token").GetTrafficUserByID(context.Background(), 66)
	if err != nil {
		t.Fatal(err)
	}
	if user.UsedTrafficBytes != 42362891 {
		t.Errorf("unexpected used traffic: %v", user.UsedTrafficBytes)
	}
}

func TestTrafficUsersStreamResponseV3(t *testing.T) {
	raw := []byte(`{
		"response": {
			"users": [{
				"id": 66,
				"username": "Service",
				"status": "LIMITED",
				"trafficLimitBytes": 36700160,
				"trafficLimitStrategy": "NO_RESET",
				"activeInternalSquads": [
					{
						"uuid": "27e0db19-b329-4802-b617-11dca0c9ec72",
						"name": "WL Limited"
					},
					{
						"uuid": "b70fffc7-5a0d-4f1b-801b-285d51707ebd",
						"name": "WL Unlimited"
					}
				],
				"userTraffic": {
					"usedTrafficBytes": 42362891,
					"lifetimeUsedTrafficBytes": 42362891
				}
			}],
			"nextCursor": null,
			"hasMore": false
		}
	}`)

	var got trafficUsersStreamResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}

	if len(got.Response.Data) != 1 {
		t.Fatalf("expected 1 user, got %d", len(got.Response.Data))
	}

	user := got.Response.Data[0]

	if user.ID != 66 {
		t.Errorf("unexpected id: %d", user.ID)
	}
	if user.Status != "LIMITED" {
		t.Errorf("unexpected status: %s", user.Status)
	}
	if user.UsedTrafficBytes != 42362891 {
		t.Errorf("unexpected used traffic: %v", user.UsedTrafficBytes)
	}
	if len(user.ActiveInternalSquads) != 2 {
		t.Errorf("unexpected squads: %v", user.ActiveInternalSquads)
	}
}

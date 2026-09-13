package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

type InternalSquad struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

type TrafficUserData struct {
	ID                   int64           `json:"id"`
	Username             string          `json:"username"`
	Status               string          `json:"status"`
	TrafficLimitBytes    float64         `json:"trafficLimitBytes"`
	UsedTrafficBytes     float64         `json:"usedTrafficBytes"`
	TrafficLimitStrategy string          `json:"trafficLimitStrategy"`
	ActiveInternalSquads []InternalSquad `json:"activeInternalSquads"`
}

type trafficUserResponse struct {
	Response TrafficUserData `json:"response"`
}

type trafficUsersStreamResponse struct {
	Response struct {
		Data       []TrafficUserData `json:"data"`
		NextCursor *int64            `json:"nextCursor"`
	} `json:"response"`
}

type UpdateTrafficUserRequest struct {
	ID                   int64    `json:"id"`
	Status               *string  `json:"status,omitempty"`
	TrafficLimitBytes    *float64 `json:"trafficLimitBytes,omitempty"`
	TrafficLimitStrategy *string  `json:"trafficLimitStrategy,omitempty"`
	ActiveInternalSquads []string `json:"activeInternalSquads,omitempty"`
}

func (c *Client) GetTrafficUserByID(ctx context.Context, userID int64) (*TrafficUserData, error) {
	data, err := c.doRequest(ctx, http.MethodGet, "/api/users/"+strconv.FormatInt(userID, 10), nil)
	if err != nil {
		return nil, fmt.Errorf("get traffic user by id %d: %w", userID, err)
	}

	var resp trafficUserResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("decode traffic user response: %w", err)
	}

	return &resp.Response, nil
}

func (c *Client) UpdateTrafficUser(ctx context.Context, req UpdateTrafficUserRequest) error {
	if req.ID <= 0 {
		return fmt.Errorf("update traffic user: invalid user id %d", req.ID)
	}

	if _, err := c.doRequest(ctx, http.MethodPatch, "/api/users", req); err != nil {
		return fmt.Errorf("update traffic user %d: %w", req.ID, err)
	}
	return nil
}

func (c *Client) ListLimitedTrafficUsers(ctx context.Context) ([]TrafficUserData, error) {
	const pageSize = 1000

	var (
		all    []TrafficUserData
		cursor *int64
	)

	for {
		path := "/api/users/stream?size=" + strconv.Itoa(pageSize) + "&status=LIMITED"
		if cursor != nil {
			path += "&cursor=" + strconv.FormatInt(*cursor, 10)
		}

		data, err := c.doRequest(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, fmt.Errorf("list LIMITED users: %w", err)
		}

		var resp trafficUsersStreamResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			return nil, fmt.Errorf("decode LIMITED users stream: %w", err)
		}

		all = append(all, resp.Response.Data...)
		if resp.Response.NextCursor == nil {
			return all, nil
		}

		if cursor != nil && *cursor == *resp.Response.NextCursor {
			return nil, fmt.Errorf("list LIMITED users: nextCursor did not advance (%d)", *cursor)
		}
		cursor = resp.Response.NextCursor
	}
}

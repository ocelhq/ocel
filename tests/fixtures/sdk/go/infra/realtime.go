package infra

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"ocel.dev/realtime"
)

type Caller struct {
	ID         string   `json:"id"`
	Orders     []string `json:"orders"`
	Projects   []string `json:"projects"`
	MayPublish bool     `json:"mayPublish"`
}

func readList(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, ",")
}

func readCaller(r *http.Request) (*Caller, error) {
	credential, isBearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !isBearer {
		return nil, nil
	}
	claims, err := url.ParseQuery(credential)
	if err != nil || claims.Get("user") == "" {
		return nil, nil
	}
	return &Caller{
		ID:         claims.Get("user"),
		Orders:     readList(claims.Get("orders")),
		Projects:   readList(claims.Get("projects")),
		MayPublish: claims.Get("publish") == "yes",
	}, nil
}

var Live = realtime.New("app", realtime.Authorize(readCaller))

type OrderEvent struct {
	Status string `json:"status"`
}

type OrderParams struct {
	OrderID string `realtime:"orderId"`
}

var Orders = realtime.NewChannel[OrderEvent, OrderParams](Live, "orders/:orderId",
	realtime.ChannelSubscribe(func(ctx *realtime.SubscribeContext[Caller, OrderParams]) (bool, error) {
		return slices.Contains(ctx.Auth.Orders, ctx.Params.OrderID), nil
	}),
)

type DeployEvent struct {
	State string `json:"state"`
}

type DeployParams struct {
	ProjectID string `realtime:"projectId"`
	DeployID  string `realtime:"deployId"`
}

var Deploys = realtime.NewChannel[DeployEvent, DeployParams](Live, "projects/:projectId/deploys/:deployId",
	realtime.ChannelWildcard(),
	realtime.ChannelSubscribe(func(ctx *realtime.SubscribeContext[Caller, DeployParams]) (bool, error) {
		return ctx.Params.ProjectID != "" && slices.Contains(ctx.Auth.Projects, ctx.Params.ProjectID), nil
	}),
)

type RoomParams struct {
	RoomID string `realtime:"roomId"`
}

const noteSchema = `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`

var Rooms = realtime.NewChannel[json.RawMessage, RoomParams](Live, "rooms/:roomId",
	realtime.Schema(noteSchema),
	realtime.ChannelSubscribe(func(*realtime.SubscribeContext[Caller, RoomParams]) (bool, error) {
		return true, nil
	}),
	realtime.ChannelPublish(func(ctx *realtime.PublishContext[Caller, RoomParams, json.RawMessage]) (bool, error) {
		return ctx.Auth.MayPublish, nil
	}),
)

var Status = realtime.NewChannel[json.RawMessage, struct{}](Live, "status",
	realtime.Schema(noteSchema),
	realtime.ChannelSubscribePublic(),
)

package console

import (
	"context"
	"errors"
	"net/http"
	"time"
)

var ErrCloudNotOffered = errors.New("this console offers no Ocel Cloud")

type CloudOffer struct {
	URL string `json:"url"`
}

type CloudToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func (c *Client) ReadCloudOffer(ctx context.Context, accessToken string) (CloudOffer, error) {
	var offer CloudOffer
	err := c.send(ctx, http.MethodGet, "/api/cloud", accessToken, nil, &offer)
	var refused *Error
	if errors.As(err, &refused) && refused.StatusCode == http.StatusNotFound {
		return CloudOffer{}, ErrCloudNotOffered
	}
	return offer, err
}

func (c *Client) IssueCloudToken(ctx context.Context, accessToken string) (CloudToken, error) {
	var issued CloudToken
	if err := c.send(ctx, http.MethodPost, "/api/cloud/token", accessToken, struct{}{}, &issued); err != nil {
		return CloudToken{}, err
	}
	return issued, nil
}

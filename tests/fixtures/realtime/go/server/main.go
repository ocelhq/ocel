package main

import (
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"example.com/realtime/infra"
	"ocel.dev"
)

const bindingKey = "OCEL_RESOURCE_REALTIME_app"

type publishRequest struct {
	Pattern string            `json:"pattern"`
	Params  map[string]string `json:"params"`
	Body    json.RawMessage   `json:"body"`
}

type signRequest struct {
	Header   json.RawMessage `json:"header"`
	Claims   json.RawMessage `json:"claims"`
	SignedBy string          `json:"signedBy"`
}

func writeAnswer(w http.ResponseWriter, status int, body any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func decodeInto[T any](raw json.RawMessage) (T, error) {
	var value T
	err := json.Unmarshal(raw, &value)
	return value, err
}

func publish(ctx context.Context, req publishRequest) error {
	switch req.Pattern {
	case infra.Orders.Pattern():
		body, err := decodeInto[infra.OrderEvent](req.Body)
		if err != nil {
			return err
		}
		return infra.Orders.Publish(ctx, infra.OrderParams{OrderID: req.Params["orderId"]}, body)
	case infra.Deploys.Pattern():
		body, err := decodeInto[infra.DeployEvent](req.Body)
		if err != nil {
			return err
		}
		params := infra.DeployParams{ProjectID: req.Params["projectId"], DeployID: req.Params["deployId"]}
		return infra.Deploys.Publish(ctx, params, body)
	case infra.Rooms.Pattern():
		return infra.Rooms.Publish(ctx, infra.RoomParams{RoomID: req.Params["roomId"]}, req.Body)
	case infra.Status.Pattern():
		return infra.Status.Publish(ctx, struct{}{}, req.Body)
	}
	return fmt.Errorf("no channel of pattern %q is declared", req.Pattern)
}

func servePublish(w http.ResponseWriter, r *http.Request) {
	var req publishRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeAnswer(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	err := publish(r.Context(), req)
	var refused *ocel.PublishRefusedError
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.As(err, &refused):
		writeAnswer(w, http.StatusUnprocessableEntity, map[string]string{"code": refused.Code})
	default:
		writeAnswer(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
	}
}

func isJourneyNonce(given string) bool {
	return given != "" && subtle.ConstantTimeCompare([]byte(given), []byte(infra.Env.JourneyNonce.Value())) == 1
}

func readBinding() ([]byte, error) {
	if dir := os.Getenv("OCEL_LIVE_DIR"); dir != "" {
		if raw, err := os.ReadFile(filepath.Join(dir, bindingKey)); err == nil {
			return raw, nil
		}
	}
	raw := os.Getenv(bindingKey)
	if raw == "" {
		return nil, fmt.Errorf("%s was not delivered", bindingKey)
	}
	return []byte(raw), nil
}

func readSigningKey(signedBy string) (ed25519.PrivateKey, error) {
	switch signedBy {
	case "binding":
		raw, err := readBinding()
		if err != nil {
			return nil, err
		}
		var delivered struct {
			Realtime struct {
				SigningKey []byte `json:"signingKey"`
			} `json:"realtime"`
		}
		if err := json.Unmarshal(raw, &delivered); err != nil {
			return nil, err
		}
		return ed25519.NewKeyFromSeed(delivered.Realtime.SigningKey), nil
	case "another-key":
		_, key, err := ed25519.GenerateKey(nil)
		return key, err
	case "nobody":
		return nil, nil
	}
	return nil, fmt.Errorf("no signer named %q", signedBy)
}

func serveTokens(w http.ResponseWriter, r *http.Request) {
	if !isJourneyNonce(r.Header.Get("x-journey-nonce")) {
		writeAnswer(w, http.StatusForbidden, map[string]string{"error": "signing a token needs the nonce the harness set"})
		return
	}
	var req signRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAnswer(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	key, err := readSigningKey(req.SignedBy)
	if err != nil {
		writeAnswer(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	encode := base64.RawURLEncoding.EncodeToString
	input := encode(req.Header) + "." + encode(req.Claims)
	signature := ""
	if key != nil {
		signature = encode(ed25519.Sign(key, []byte(input)))
	}
	writeAnswer(w, http.StatusOK, map[string]string{"token": input + "." + signature})
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3114"
	}

	http.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeAnswer(w, http.StatusOK, map[string]any{"ok": true, "app": "web"})
	})
	http.Handle("/api/realtime", infra.Live.Handler())
	http.HandleFunc("POST /api/publish", servePublish)
	http.HandleFunc("POST /api/tokens", serveTokens)

	log.Printf("realtime fixture listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

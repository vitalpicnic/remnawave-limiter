package prepaid

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

const maxWebhookBodyBytes = 1 << 20 // 1 MiB

type WebhookPayload struct {
	Scope     string          `json:"scope"`
	Event     string          `json:"event"`
	Timestamp string          `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

type webhookUserData struct {
	ID int64 `json:"id"`
}

type HTTPServer struct {
	addr    string
	secret  string
	service *Service
	logger  *logrus.Logger
	server  *http.Server
}

func NewHTTPServer(addr, secret string, service *Service, logger *logrus.Logger) *HTTPServer {
	s := &HTTPServer{
		addr:    addr,
		secret:  secret,
		service: service,
		logger:  logger,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/remnawave/webhook", s.handleWebhook)
	mux.HandleFunc("/healthz", s.handleHealth)

	s.server = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return s
}

func (s *HTTPServer) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.logger.WithField("addr", s.addr).Info("Prepaid webhook HTTP server запущен")
		errCh <- s.server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown prepaid HTTP server: %w", err)
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *HTTPServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *HTTPServer) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBodyBytes+1))
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}
	if len(body) > maxWebhookBodyBytes {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}

	signature := r.Header.Get("X-Remnawave-Signature")
	if !VerifyWebhookSignature(body, signature, s.secret) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	var payload WebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	if payload.Scope != "" && payload.Scope != "user" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	switch payload.Event {
	case "user.limited", "user.modified", "user.traffic_reset":
		var user webhookUserData
		if err := json.Unmarshal(payload.Data, &user); err != nil {
			http.Error(w, "invalid user data", http.StatusBadRequest)
			return
		}
		if user.ID <= 0 {
			http.Error(w, "missing user id", http.StatusBadRequest)
			return
		}

		if err := s.service.HandleEvent(r.Context(), payload.Event, user.ID); err != nil {
			s.logger.WithError(err).WithFields(logrus.Fields{
				"event":   payload.Event,
				"user_id": user.ID,
			}).Error("Ошибка обработки Remnawave webhook")
			http.Error(w, "processing failed", http.StatusInternalServerError)
			return
		}
	default:
		// Other Remnawave events are intentionally ignored by this module.
	}

	w.WriteHeader(http.StatusNoContent)
}

func VerifyWebhookSignature(body []byte, signature, secret string) bool {
	if len(body) == 0 || strings.TrimSpace(signature) == "" || secret == "" {
		return false
	}

	got, err := hex.DecodeString(strings.TrimSpace(signature))
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	expected := mac.Sum(nil)
	return hmac.Equal(got, expected)
}

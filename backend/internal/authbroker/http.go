package authbroker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const brokerTokenHeader = "X-Botbox-Broker-Token" //nolint:gosec // HTTP header name, not a credential

type errorResponse struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

func mapServiceError(err error) (int, string, string) {
	switch {
	case errors.Is(err, ErrSessionNotFound):
		return http.StatusNotFound, "session_not_found", err.Error()
	case errors.Is(err, ErrSessionExpired):
		return http.StatusGone, "session_expired", err.Error()
	case errors.Is(err, ErrSessionAlreadyUsed):
		return http.StatusConflict, "session_already_used", err.Error()
	case errors.Is(err, ErrBindingMismatch):
		return http.StatusConflict, "binding_mismatch", err.Error()
	case errors.Is(err, ErrTakeoverActive):
		return http.StatusLocked, "takeover_active", err.Error()
	case errors.Is(err, ErrCaptureSuppressed):
		return http.StatusConflict, "capture_suppressed", err.Error()
	case errors.Is(err, ErrTakeoverForbidden):
		return http.StatusForbidden, "takeover_forbidden", err.Error()
	case errors.Is(err, ErrTakeoverNotActive):
		return http.StatusConflict, "takeover_not_active", err.Error()
	case errors.Is(err, ErrNavigationForbidden):
		return http.StatusForbidden, "navigation_forbidden", err.Error()
	case errors.Is(err, ErrActionForbidden):
		return http.StatusForbidden, "action_forbidden", err.Error()
	case errors.Is(err, ErrAdapterNotFound):
		return http.StatusUnprocessableEntity, "adapter_not_found", err.Error()
	default:
		return http.StatusInternalServerError, "broker_error", "broker operation failed"
	}
}

func errorFromCode(code, msg string) error {
	switch code {
	case "session_not_found":
		return ErrSessionNotFound
	case "session_expired":
		return ErrSessionExpired
	case "session_already_used":
		return ErrSessionAlreadyUsed
	case "binding_mismatch":
		return ErrBindingMismatch
	case "takeover_active":
		return ErrTakeoverActive
	case "capture_suppressed":
		return ErrCaptureSuppressed
	case "takeover_forbidden":
		return ErrTakeoverForbidden
	case "takeover_not_active":
		return ErrTakeoverNotActive
	case "navigation_forbidden":
		return ErrNavigationForbidden
	case "action_forbidden":
		return ErrActionForbidden
	case "adapter_not_found":
		return ErrAdapterNotFound
	default:
		if msg == "" {
			msg = "broker request failed"
		}
		return errors.New(msg)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeBrokerErr(w http.ResponseWriter, err error) {
	status, code, msg := mapServiceError(err)
	writeJSON(w, status, errorResponse{Code: code, Error: msg})
}

func decodeJSONBody(r *http.Request, dst any) error {
	defer r.Body.Close() //nolint:errcheck // best-effort close
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return nil
}

// NewHTTPHandler mounts the authenticated isolated broker control-plane API.
func NewHTTPHandler(client Client, authToken string) (http.Handler, error) {
	if client == nil {
		return nil, errors.New("broker client is required")
	}
	trimmed := strings.TrimSpace(authToken)
	if len(trimmed) < 16 {
		return nil, errors.New("broker auth token must be at least 16 characters")
	}
	expectedDigest := sha256.Sum256([]byte(trimmed))

	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Route("/broker/v1", func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				provided := sha256.Sum256([]byte(strings.TrimSpace(req.Header.Get(brokerTokenHeader))))
				if subtle.ConstantTimeCompare(provided[:], expectedDigest[:]) != 1 {
					writeJSON(w, http.StatusUnauthorized, errorResponse{Code: "unauthorized", Error: "invalid broker token"})
					return
				}
				next.ServeHTTP(w, req)
			})
		})

		r.Post("/sessions", func(w http.ResponseWriter, req *http.Request) {
			var in OpenSessionInput
			if err := decodeJSONBody(req, &in); err != nil {
				writeJSON(w, http.StatusBadRequest, errorResponse{Code: "bad_request", Error: "invalid payload"})
				return
			}
			sess, err := client.OpenSession(req.Context(), in)
			if err != nil {
				writeBrokerErr(w, err)
				return
			}
			writeJSON(w, http.StatusOK, sess)
		})

		r.Delete("/workspaces/{workspaceID}/bots/{botID}/sessions", func(w http.ResponseWriter, req *http.Request) {
			wsID, err := uuid.Parse(chi.URLParam(req, "workspaceID"))
			if err != nil {
				writeJSON(w, http.StatusBadRequest, errorResponse{Code: "bad_request", Error: "invalid workspaceID"})
				return
			}
			botID, err := uuid.Parse(chi.URLParam(req, "botID"))
			if err != nil {
				writeJSON(w, http.StatusBadRequest, errorResponse{Code: "bad_request", Error: "invalid botID"})
				return
			}
			if err := client.RevokeBotSessions(req.Context(), wsID, botID); err != nil {
				writeBrokerErr(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})

		r.Route("/workspaces/{workspaceID}/sessions/{sessionID}", func(r chi.Router) {
			parseIDs := func(req *http.Request) (uuid.UUID, uuid.UUID, error) {
				wsID, err := uuid.Parse(chi.URLParam(req, "workspaceID"))
				if err != nil {
					return uuid.Nil, uuid.Nil, err
				}
				sessID, err := uuid.Parse(chi.URLParam(req, "sessionID"))
				if err != nil {
					return uuid.Nil, uuid.Nil, err
				}
				return wsID, sessID, nil
			}

			r.Get("/", func(w http.ResponseWriter, req *http.Request) {
				wsID, sessID, err := parseIDs(req)
				if err != nil {
					writeJSON(w, http.StatusBadRequest, errorResponse{Code: "bad_request", Error: "invalid identifier"})
					return
				}
				sess, err := client.GetSession(req.Context(), wsID, sessID)
				if err != nil {
					writeBrokerErr(w, err)
					return
				}
				writeJSON(w, http.StatusOK, sess)
			})

			r.Delete("/", func(w http.ResponseWriter, req *http.Request) {
				wsID, sessID, err := parseIDs(req)
				if err != nil {
					writeJSON(w, http.StatusBadRequest, errorResponse{Code: "bad_request", Error: "invalid identifier"})
					return
				}
				if err := client.RevokeSession(req.Context(), wsID, sessID); err != nil {
					writeBrokerErr(w, err)
					return
				}
				w.WriteHeader(http.StatusNoContent)
			})

			r.Post("/submit", func(w http.ResponseWriter, req *http.Request) {
				wsID, sessID, err := parseIDs(req)
				if err != nil {
					writeJSON(w, http.StatusBadRequest, errorResponse{Code: "bad_request", Error: "invalid identifier"})
					return
				}
				var in SubmitCredentialsInput
				if err := decodeJSONBody(req, &in); err != nil {
					writeJSON(w, http.StatusBadRequest, errorResponse{Code: "bad_request", Error: "invalid payload"})
					return
				}
				in.WorkspaceID = wsID
				in.SessionID = sessID
				res, err := client.SubmitCredentials(req.Context(), in)
				if err != nil {
					writeBrokerErr(w, err)
					return
				}
				writeJSON(w, http.StatusOK, res)
			})

			r.Post("/navigate", func(w http.ResponseWriter, req *http.Request) {
				wsID, sessID, err := parseIDs(req)
				if err != nil {
					writeJSON(w, http.StatusBadRequest, errorResponse{Code: "bad_request", Error: "invalid identifier"})
					return
				}
				var body struct {
					BotID uuid.UUID `json:"bot_id"`
					URL   string    `json:"url"`
				}
				if err := decodeJSONBody(req, &body); err != nil {
					writeJSON(w, http.StatusBadRequest, errorResponse{Code: "bad_request", Error: "invalid payload"})
					return
				}
				sess, err := client.BotNavigate(req.Context(), wsID, body.BotID, sessID, body.URL)
				if err != nil {
					writeBrokerErr(w, err)
					return
				}
				writeJSON(w, http.StatusOK, sess)
			})

			r.Post("/read", func(w http.ResponseWriter, req *http.Request) {
				wsID, sessID, err := parseIDs(req)
				if err != nil {
					writeJSON(w, http.StatusBadRequest, errorResponse{Code: "bad_request", Error: "invalid identifier"})
					return
				}
				var body struct {
					BotID    uuid.UUID `json:"bot_id"`
					Selector string    `json:"selector"`
				}
				if err := decodeJSONBody(req, &body); err != nil {
					writeJSON(w, http.StatusBadRequest, errorResponse{Code: "bad_request", Error: "invalid payload"})
					return
				}
				res, err := client.BotRead(req.Context(), wsID, body.BotID, sessID, body.Selector)
				if err != nil {
					writeBrokerErr(w, err)
					return
				}
				writeJSON(w, http.StatusOK, res)
			})

			r.Post("/click", func(w http.ResponseWriter, req *http.Request) {
				wsID, sessID, err := parseIDs(req)
				if err != nil {
					writeJSON(w, http.StatusBadRequest, errorResponse{Code: "bad_request", Error: "invalid identifier"})
					return
				}
				var body struct {
					BotID    uuid.UUID `json:"bot_id"`
					Selector string    `json:"selector"`
				}
				if err := decodeJSONBody(req, &body); err != nil {
					writeJSON(w, http.StatusBadRequest, errorResponse{Code: "bad_request", Error: "invalid payload"})
					return
				}
				res, err := client.BotClick(req.Context(), wsID, body.BotID, sessID, body.Selector)
				if err != nil {
					writeBrokerErr(w, err)
					return
				}
				writeJSON(w, http.StatusOK, res)
			})

			r.Post("/screenshot", func(w http.ResponseWriter, req *http.Request) {
				wsID, sessID, err := parseIDs(req)
				if err != nil {
					writeJSON(w, http.StatusBadRequest, errorResponse{Code: "bad_request", Error: "invalid identifier"})
					return
				}
				var body struct {
					BotID uuid.UUID `json:"bot_id"`
				}
				if err := decodeJSONBody(req, &body); err != nil {
					writeJSON(w, http.StatusBadRequest, errorResponse{Code: "bad_request", Error: "invalid payload"})
					return
				}
				img, binding, err := client.BotScreenshot(req.Context(), wsID, body.BotID, sessID)
				if err != nil {
					writeBrokerErr(w, err)
					return
				}
				bJSON, _ := json.Marshal(binding)
				w.Header().Set("Content-Type", "image/png")
				w.Header().Set("X-Broker-Binding", string(bJSON))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(img) //nolint:gosec // fixed Content-Type image/png binary payload
			})

			r.Post("/takeover/start", func(w http.ResponseWriter, req *http.Request) {
				wsID, sessID, err := parseIDs(req)
				if err != nil {
					writeJSON(w, http.StatusBadRequest, errorResponse{Code: "bad_request", Error: "invalid identifier"})
					return
				}
				var body struct {
					UserID uuid.UUID `json:"user_id"`
				}
				if err := decodeJSONBody(req, &body); err != nil {
					writeJSON(w, http.StatusBadRequest, errorResponse{Code: "bad_request", Error: "invalid payload"})
					return
				}
				sess, err := client.StartTakeover(req.Context(), wsID, sessID, body.UserID)
				if err != nil {
					writeBrokerErr(w, err)
					return
				}
				writeJSON(w, http.StatusOK, sess)
			})

			r.Get("/takeover/frame", func(w http.ResponseWriter, req *http.Request) {
				wsID, sessID, err := parseIDs(req)
				if err != nil {
					writeJSON(w, http.StatusBadRequest, errorResponse{Code: "bad_request", Error: "invalid identifier"})
					return
				}
				userID, err := uuid.Parse(strings.TrimSpace(req.URL.Query().Get("user_id")))
				if err != nil {
					writeJSON(w, http.StatusBadRequest, errorResponse{Code: "bad_request", Error: "invalid user_id"})
					return
				}
				img, info, err := client.TakeoverFrame(req.Context(), wsID, sessID, userID)
				if err != nil {
					writeBrokerErr(w, err)
					return
				}
				sJSON, _ := json.Marshal(info)
				w.Header().Set("Content-Type", "image/png")
				w.Header().Set("X-Broker-Session", string(sJSON))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(img) //nolint:gosec // fixed Content-Type image/png binary payload
			})

			r.Post("/takeover/input", func(w http.ResponseWriter, req *http.Request) {
				wsID, sessID, err := parseIDs(req)
				if err != nil {
					writeJSON(w, http.StatusBadRequest, errorResponse{Code: "bad_request", Error: "invalid identifier"})
					return
				}
				var body struct {
					UserID uuid.UUID     `json:"user_id"`
					Input  TakeoverInput `json:"input"`
				}
				if err := decodeJSONBody(req, &body); err != nil {
					writeJSON(w, http.StatusBadRequest, errorResponse{Code: "bad_request", Error: "invalid payload"})
					return
				}
				sess, err := client.TakeoverInput(req.Context(), wsID, sessID, body.UserID, body.Input)
				if err != nil {
					writeBrokerErr(w, err)
					return
				}
				writeJSON(w, http.StatusOK, sess)
			})

			r.Post("/takeover/complete", func(w http.ResponseWriter, req *http.Request) {
				wsID, sessID, err := parseIDs(req)
				if err != nil {
					writeJSON(w, http.StatusBadRequest, errorResponse{Code: "bad_request", Error: "invalid identifier"})
					return
				}
				var body struct {
					UserID uuid.UUID `json:"user_id"`
				}
				if err := decodeJSONBody(req, &body); err != nil {
					writeJSON(w, http.StatusBadRequest, errorResponse{Code: "bad_request", Error: "invalid payload"})
					return
				}
				sess, err := client.CompleteTakeover(req.Context(), wsID, sessID, body.UserID)
				if err != nil {
					writeBrokerErr(w, err)
					return
				}
				writeJSON(w, http.StatusOK, sess)
			})
		})
	})

	return r, nil
}

// HTTPClient speaks the authenticated broker HTTP control plane from the API service.
type HTTPClient struct {
	baseURL    string
	authToken  string
	httpClient *http.Client
}

// NewHTTPClient creates an HTTPClient configured with an isolated broker base URL and shared token.
func NewHTTPClient(baseURL, authToken string) (*HTTPClient, error) {
	trimmedURL := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	trimmedToken := strings.TrimSpace(authToken)
	if trimmedURL == "" || len(trimmedToken) < 16 {
		return nil, errors.New("broker baseURL and >=16-char authToken are required")
	}
	return &HTTPClient{
		baseURL:    trimmedURL,
		authToken:  trimmedToken,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (c *HTTPClient) doJSON(ctx context.Context, method, path string, in, out any) error {
	var bodyReader io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader) //nolint:gosec // c.baseURL is service-configured isolated broker endpoint
	if err != nil {
		return err
	}
	req.Header.Set(brokerTokenHeader, c.authToken)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req) //nolint:gosec // c.baseURL is service-configured isolated broker endpoint
	if err != nil {
		return fmt.Errorf("broker request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // best-effort close

	if resp.StatusCode >= 400 {
		var er errorResponse
		if decErr := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&er); decErr == nil && er.Code != "" {
			return errorFromCode(er.Code, er.Error)
		}
		return fmt.Errorf("broker returned HTTP %d", resp.StatusCode)
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
	}
	return nil
}

func (c *HTTPClient) doBinary(ctx context.Context, method, path string, in any) ([]byte, http.Header, error) {
	var bodyReader io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return nil, nil, err
		}
		bodyReader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader) //nolint:gosec // c.baseURL is service-configured isolated broker endpoint
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set(brokerTokenHeader, c.authToken)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req) //nolint:gosec // c.baseURL is service-configured isolated broker endpoint
	if err != nil {
		return nil, nil, fmt.Errorf("broker binary request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // best-effort close

	if resp.StatusCode >= 400 {
		var er errorResponse
		if decErr := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&er); decErr == nil && er.Code != "" {
			return nil, nil, errorFromCode(er.Code, er.Error)
		}
		return nil, nil, fmt.Errorf("broker returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, nil, err
	}
	return data, resp.Header, nil
}

// OpenSession opens or attaches an isolated target in the remote broker.
func (c *HTTPClient) OpenSession(ctx context.Context, in OpenSessionInput) (SessionInfo, error) {
	var out SessionInfo
	err := c.doJSON(ctx, http.MethodPost, "/broker/v1/sessions", in, &out)
	return out, err
}

// GetSession reads current session metadata from the remote broker.
func (c *HTTPClient) GetSession(ctx context.Context, workspaceID, sessionID uuid.UUID) (SessionInfo, error) {
	var out SessionInfo
	err := c.doJSON(ctx, http.MethodGet, fmt.Sprintf("/broker/v1/workspaces/%s/sessions/%s", workspaceID, sessionID), nil, &out)
	return out, err
}

// SubmitCredentials dispatches a user's credential solely to the remote broker fill endpoint.
func (c *HTTPClient) SubmitCredentials(ctx context.Context, in SubmitCredentialsInput) (SubmitCredentialsResult, error) {
	var out SubmitCredentialsResult
	err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/broker/v1/workspaces/%s/sessions/%s/submit", in.WorkspaceID, in.SessionID), in, &out)
	return out, err
}

// BotNavigate navigates an isolated broker session within adapter-approved HTTPS origins.
func (c *HTTPClient) BotNavigate(ctx context.Context, workspaceID, botID, sessionID uuid.UUID, rawURL string) (SessionInfo, error) {
	var out SessionInfo
	err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/broker/v1/workspaces/%s/sessions/%s/navigate", workspaceID, sessionID), map[string]any{
		"bot_id": botID,
		"url":    rawURL,
	}, &out)
	return out, err
}

// BotRead retrieves sanitized selector text from the remote broker.
func (c *HTTPClient) BotRead(ctx context.Context, workspaceID, botID, sessionID uuid.UUID, selector string) (ReadResult, error) {
	var out ReadResult
	err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/broker/v1/workspaces/%s/sessions/%s/read", workspaceID, sessionID), map[string]any{
		"bot_id":   botID,
		"selector": selector,
	}, &out)
	return out, err
}

// BotClick clicks a permitted non-credential element in the remote broker.
func (c *HTTPClient) BotClick(ctx context.Context, workspaceID, botID, sessionID uuid.UUID, selector string) (ClickResult, error) {
	var out ClickResult
	err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/broker/v1/workspaces/%s/sessions/%s/click", workspaceID, sessionID), map[string]any{
		"bot_id":   botID,
		"selector": selector,
	}, &out)
	return out, err
}

// BotScreenshot captures a PNG viewport image when capture is not suppressed.
func (c *HTTPClient) BotScreenshot(ctx context.Context, workspaceID, botID, sessionID uuid.UUID) ([]byte, TargetBinding, error) {
	data, hdr, err := c.doBinary(ctx, http.MethodPost, fmt.Sprintf("/broker/v1/workspaces/%s/sessions/%s/screenshot", workspaceID, sessionID), map[string]any{
		"bot_id": botID,
	})
	if err != nil {
		return nil, TargetBinding{}, err
	}
	var binding TargetBinding
	_ = json.Unmarshal([]byte(hdr.Get("X-Broker-Binding")), &binding)
	return data, binding, nil
}

// StartTakeover initiates user-pinned human takeover on the remote broker.
func (c *HTTPClient) StartTakeover(ctx context.Context, workspaceID, sessionID, userID uuid.UUID) (SessionInfo, error) {
	var out SessionInfo
	err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/broker/v1/workspaces/%s/sessions/%s/takeover/start", workspaceID, sessionID), map[string]any{
		"user_id": userID,
	}, &out)
	return out, err
}

// TakeoverFrame fetches a live PNG viewport frame exclusively for the takeover owner.
func (c *HTTPClient) TakeoverFrame(ctx context.Context, workspaceID, sessionID, userID uuid.UUID) ([]byte, SessionInfo, error) {
	data, hdr, err := c.doBinary(ctx, http.MethodGet, fmt.Sprintf("/broker/v1/workspaces/%s/sessions/%s/takeover/frame?user_id=%s", workspaceID, sessionID, userID), nil)
	if err != nil {
		return nil, SessionInfo{}, err
	}
	var info SessionInfo
	_ = json.Unmarshal([]byte(hdr.Get("X-Broker-Session")), &info)
	return data, info, nil
}

// TakeoverInput sends a human UI event to the remote broker during takeover.
func (c *HTTPClient) TakeoverInput(ctx context.Context, workspaceID, sessionID, userID uuid.UUID, input TakeoverInput) (SessionInfo, error) {
	var out SessionInfo
	err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/broker/v1/workspaces/%s/sessions/%s/takeover/input", workspaceID, sessionID), map[string]any{
		"user_id": userID,
		"input":   input,
	}, &out)
	return out, err
}

// CompleteTakeover concludes human takeover on the remote broker and verifies allowed origin.
func (c *HTTPClient) CompleteTakeover(ctx context.Context, workspaceID, sessionID, userID uuid.UUID) (SessionInfo, error) {
	var out SessionInfo
	err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/broker/v1/workspaces/%s/sessions/%s/takeover/complete", workspaceID, sessionID), map[string]any{
		"user_id": userID,
	}, &out)
	return out, err
}

// RevokeSession closes and removes a single session on the remote broker.
func (c *HTTPClient) RevokeSession(ctx context.Context, workspaceID, sessionID uuid.UUID) error {
	return c.doJSON(ctx, http.MethodDelete, fmt.Sprintf("/broker/v1/workspaces/%s/sessions/%s", workspaceID, sessionID), nil, nil)
}

// RevokeBotSessions revokes all broker sessions belonging to botID on the remote broker.
func (c *HTTPClient) RevokeBotSessions(ctx context.Context, workspaceID, botID uuid.UUID) error {
	return c.doJSON(ctx, http.MethodDelete, fmt.Sprintf("/broker/v1/workspaces/%s/bots/%s/sessions", workspaceID, botID), nil, nil)
}

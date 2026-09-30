package billingcenter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type TokenSource interface {
	Token(context.Context) (string, error)
}

type Config struct {
	BaseURL          string
	ProducerClientID string
	Timeout          time.Duration
	// InsecureLocal permits loopback HTTP for isolated development/tests only.
	InsecureLocal bool
}

// Reservation contains the remote authority's frozen settlement context.
// Amounts remain decimal strings all the way to callers and durable storage.
type Reservation struct {
	OperationID        string    `json:"operation_id"`
	ReservationID      string    `json:"reservation_id"`
	State              string    `json:"state"`
	Version            int64     `json:"version"`
	BillingAccountID   string    `json:"billing_account_id"`
	MemberAllocationID string    `json:"member_allocation_id,omitempty"`
	OwnerEpoch         int64     `json:"owner_epoch"`
	WalletUnit         string    `json:"wallet_unit"`
	ReservedAmount     Decimal   `json:"reserved_amount"`
	SettledAmount      Decimal   `json:"settled_amount"`
	PriceVersionID     string    `json:"price_version_id"`
	ProductVersion     string    `json:"product_version"`
	BalanceID          string    `json:"balance_id"`
	PeriodIDs          []string  `json:"period_ids"`
	ExpiresAt          time.Time `json:"expires_at"`
}

// RemoteError deliberately excludes response bodies, URLs and credentials.
// UnknownOutcome means that a POST may have committed remotely. Query/retry the
// same operation; never create a replacement operation or debit locally.
type RemoteError struct {
	Status         int
	Retryable      bool
	UnknownOutcome bool
}

func (e *RemoteError) Error() string {
	return fmt.Sprintf("billing center request failed (status=%d, outcome_unknown=%t)", e.Status, e.UnknownOutcome)
}

func IsRetryable(err error) bool {
	var remote *RemoteError
	return errors.As(err, &remote) && remote.Retryable
}

type Client struct {
	base     *url.URL
	http     *http.Client
	tokens   TokenSource
	timeout  time.Duration
	producer string
}

func NewClient(cfg Config, tokens TokenSource, transport http.RoundTripper) (*Client, error) {
	base, err := validatedURL(cfg.BaseURL, cfg.InsecureLocal)
	if err != nil {
		return nil, err
	}
	if tokens == nil {
		return nil, errors.New("billing service token source is required")
	}
	if strings.TrimSpace(cfg.ProducerClientID) == "" {
		return nil, errors.New("billing producer client ID is required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &Client{base: base, tokens: tokens, timeout: cfg.Timeout, producer: cfg.ProducerClientID, http: &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func validatedURL(raw string, insecureLocal bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid billing endpoint URL")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || !insecureLocal || !(u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()) {
			return nil, errors.New("billing endpoints require HTTPS")
		}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

// Reserve and Extend accept the HTTP contract from Auth. Subject proof belongs
// only in these transient bodies, never in the service Authorization header.
func (c *Client) Reserve(ctx context.Context, request ProofRequest[ReserveRequest]) (Reservation, error) {
	if err := request.Validate(); err != nil {
		return Reservation{}, err
	}
	body, err := json.Marshal(request)
	if err != nil {
		return Reservation{}, err
	}
	return c.request(ctx, http.MethodPost, "/internal/billing/v1/reservations", nil, body)
}
func (c *Client) Extend(ctx context.Context, id string, request ProofRequest[ExtendRequest]) (Reservation, error) {
	if err := request.Validate(); err != nil {
		return Reservation{}, err
	}
	body, err := json.Marshal(request)
	if err != nil {
		return Reservation{}, err
	}
	return c.request(ctx, http.MethodPost, reservationPath(id, "extend"), nil, body)
}
func (c *Client) Dispatch(ctx context.Context, key Key, id string, request TransitionRequest) (Reservation, error) {
	if key.ProducerClientID != c.producer {
		return Reservation{}, ErrConflict
	}
	body, err := json.Marshal(request)
	if err != nil {
		return Reservation{}, err
	}
	return c.request(ctx, http.MethodPost, reservationPath(id, "dispatch"), originQuery(key), body)
}
func (c *Client) Deliver(ctx context.Context, event Event) (Reservation, error) {
	if event.ProducerClientID != c.producer {
		return Reservation{}, ErrConflict
	}
	if err := event.Validate(); err != nil {
		return Reservation{}, err
	}
	return c.request(ctx, http.MethodPost, reservationPath(event.ReservationID, string(event.Kind)), originQuery(event.Key), event.Body)
}
func (c *Client) GetOperation(ctx context.Context, key Key) (Reservation, error) {
	if key.ProducerClientID != c.producer {
		return Reservation{}, ErrConflict
	}
	if err := key.Validate(); err != nil {
		return Reservation{}, err
	}
	return c.request(ctx, http.MethodGet, "/internal/billing/v1/operations/"+url.PathEscape(key.OperationID), originQuery(key), nil)
}
func originQuery(key Key) url.Values { return url.Values{"origin_app_id": {key.OriginAppID}} }
func reservationPath(id, action string) string {
	return "/internal/billing/v1/reservations/" + url.PathEscape(id) + "/" + action
}

func (c *Client) request(ctx context.Context, method, path string, query url.Values, body json.RawMessage) (Reservation, error) {
	var result Reservation
	data, err := c.requestJSON(ctx, method, path, query, body)
	if err != nil {
		return result, err
	}
	if json.Unmarshal(data, &result) != nil || result.ReservationID == "" || result.OperationID == "" {
		return result, &RemoteError{Retryable: true, UnknownOutcome: method != http.MethodGet}
	}
	return result, nil
}

func (c *Client) requestJSON(ctx context.Context, method, path string, query url.Values, body json.RawMessage) ([]byte, error) {
	var result []byte
	if len(body) > 1024*1024 || len(body) > 0 && !json.Valid(body) {
		return result, errors.New("invalid billing request body")
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	token, err := c.tokens.Token(ctx)
	if err != nil || strings.TrimSpace(token) == "" {
		// Token-source errors can contain sensitive provider responses.
		return result, &RemoteError{Retryable: true}
	}
	endpoint := strings.TrimRight(c.base.String(), "/") + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return result, errors.New("invalid billing request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(req)
	if err != nil {
		return result, &RemoteError{Retryable: true, UnknownOutcome: method != http.MethodGet}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		if response.StatusCode == http.StatusUnauthorized {
			if invalidator, ok := c.tokens.(interface{ Invalidate() }); ok {
				invalidator.Invalidate()
			}
			return result, &RemoteError{Status: response.StatusCode, Retryable: true}
		}
		retry := response.StatusCode == 408 || response.StatusCode == 429 || response.StatusCode >= 500
		return result, &RemoteError{Status: response.StatusCode, Retryable: retry, UnknownOutcome: retry && method != http.MethodGet}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
	if err != nil || len(data) > 1024*1024 || !json.Valid(data) {
		return nil, &RemoteError{Status: response.StatusCode, Retryable: true, UnknownOutcome: method != http.MethodGet}
	}
	return data, nil
}

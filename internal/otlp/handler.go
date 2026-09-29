package otlp

import (
	"bytes"
	"compress/gzip"
	"container/list"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	responseBytes = 64 << 10
	protobufType  = "application/x-protobuf"
)

// Handler is shared by all three signals on one replica. The concurrency bound
// covers body reads, decompression and upstream response reads, with no queue.
type Handler struct {
	cfg       Config
	upstream  *url.URL
	transport *http.Transport
	slots     chan struct{}
	limits    ipLimits
	proxies   []netip.Prefix
	keys      [][sha256.Size]byte
}

// New constructs an enabled handler; defaults come from the config schema.
func New(cfg Config) (*Handler, error) {
	if err := cfg.Validate(); err != nil || cfg.URL == "" {
		return nil, ErrConfig
	}

	target, _ := url.Parse(strings.TrimRight(cfg.URL, "/"))

	h := &Handler{
		cfg: cfg, upstream: target, slots: make(chan struct{}, cfg.Concurrent),
		limits: ipLimits{entries: make(map[netip.Addr]*list.Element), cfg: cfg},
		transport: &http.Transport{
			DialContext:         (&net.Dialer{Timeout: cfg.Timeout}).DialContext,
			TLSHandshakeTimeout: cfg.Timeout, ResponseHeaderTimeout: cfg.Timeout,
			MaxConnsPerHost: int(cfg.Concurrent), MaxIdleConnsPerHost: int(cfg.Concurrent),
			IdleConnTimeout: time.Minute, DisableCompression: true,
		},
	}
	for _, proxy := range cfg.TrustedProxies {
		prefix, _ := netip.ParsePrefix(proxy)
		h.proxies = append(h.proxies, prefix)
	}

	for _, key := range cfg.Keys {
		h.keys = append(h.keys, sha256.Sum256([]byte(key.Reveal())))
	}

	return h, nil
}

// Close releases idle upstream connections during shutdown.
func (h *Handler) Close() { h.transport.CloseIdleConnections() }

// ServeHTTP expects /v1/logs, /v1/metrics or /v1/traces after prefix stripping.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/logs" && r.URL.Path != "/v1/metrics" && r.URL.Path != "/v1/traces" {
		h.reject(w, r, http.StatusNotFound, "unknown OTLP signal")
		return
	}

	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Expose-Headers", "Retry-After")

	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Encoding, Authorization")
		w.Header().Set("Access-Control-Max-Age", "600")
		w.WriteHeader(http.StatusNoContent)

		return
	}

	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST, OPTIONS")
		h.reject(w, r, http.StatusMethodNotAllowed, "OTLP requires POST")

		return
	}

	if code := h.limits.admit(h.clientIP(r), time.Now()); code != 0 {
		h.reject(w, r, code, "OTLP admission rate or address capacity exceeded")
		return
	}

	if !h.authorized(r) {
		h.reject(w, r, http.StatusUnauthorized, "ingest key required")
		return
	}

	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		h.reject(w, r, http.StatusServiceUnavailable, "OTLP admission capacity exceeded")
		return
	}

	h.forward(w, r)
}

func (h *Handler) authorized(r *http.Request) bool {
	if len(h.keys) == 0 {
		return true
	}

	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return false
	}

	hash := sha256.Sum256([]byte(token))

	matched := 0
	for _, key := range h.keys {
		matched |= subtle.ConstantTimeCompare(hash[:], key[:])
	}

	return matched == 1
}

func (h *Handler) forward(w http.ResponseWriter, r *http.Request) {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (contentType != "application/json" && contentType != protobufType) {
		h.reject(w, r, http.StatusUnsupportedMediaType, "unsupported OTLP content type")
		return
	}

	deadline := time.Now().Add(h.cfg.Timeout)
	control := http.NewResponseController(w)
	_ = control.SetReadDeadline(deadline)

	defer func() { _ = control.SetReadDeadline(time.Time{}) }()

	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	defer cancel()

	body, code := h.readBody(w, r)
	if code != 0 {
		h.reject(w, r, code, "invalid or oversized OTLP body")
		return
	}

	target := *h.upstream
	target.Path = strings.TrimRight(target.Path, "/") + r.URL.Path
	target.RawPath = ""

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		h.reject(w, r, http.StatusBadGateway, "invalid Collector request")
		return
	}

	// Only protocol headers cross this boundary. Cookies, credentials, baggage,
	// forwarding headers and query parameters are never passed to the Collector.
	request.Header.Set("Content-Type", contentType)

	response, err := h.transport.RoundTrip(request)
	if err != nil {
		h.reject(w, r, http.StatusServiceUnavailable, "Collector unavailable")
		return
	}
	defer response.Body.Close()

	result, err := io.ReadAll(io.LimitReader(response.Body, responseBytes+1))
	if err != nil || len(result) > responseBytes || (response.StatusCode >= 300 && response.StatusCode < 400) {
		h.reject(w, r, http.StatusBadGateway, "invalid Collector response")
		return
	}

	writeCollectorResponse(w, response, contentType, result)
}

func writeCollectorResponse(w http.ResponseWriter, response *http.Response, contentType string, result []byte) {
	w.Header().Set("Content-Type", contentType)

	if retry := response.Header.Get("Retry-After"); retry != "" {
		w.Header().Set("Retry-After", retry)
	}

	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(result)
}

func (h *Handler) readBody(w http.ResponseWriter, r *http.Request) ([]byte, int) {
	bounded := http.MaxBytesReader(w, r.Body, h.cfg.BodyBytes)
	defer bounded.Close()

	var reader io.Reader = bounded

	switch r.Header.Get("Content-Encoding") {
	case "", "identity":
	case "gzip":
		decoded, err := gzip.NewReader(bounded)
		if err != nil {
			return nil, http.StatusBadRequest
		}
		defer decoded.Close()

		reader = decoded
	default:
		return nil, http.StatusUnsupportedMediaType
	}

	body, err := io.ReadAll(io.LimitReader(reader, h.cfg.BodyBytes+1))

	var limit *http.MaxBytesError
	if errors.As(err, &limit) || int64(len(body)) > h.cfg.BodyBytes {
		return nil, http.StatusRequestEntityTooLarge
	}

	if err != nil {
		return nil, http.StatusBadRequest
	}

	return body, 0
}

func (h *Handler) reject(w http.ResponseWriter, r *http.Request, code int, message string) {
	statusCode := codes.InvalidArgument

	switch code {
	case http.StatusUnauthorized:
		statusCode = codes.Unauthenticated
	case http.StatusTooManyRequests, http.StatusRequestEntityTooLarge:
		statusCode = codes.ResourceExhausted
	case http.StatusServiceUnavailable, http.StatusBadGateway:
		statusCode = codes.Unavailable
	}

	if code == http.StatusServiceUnavailable || code == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "1")
	}

	if code == http.StatusTooManyRequests {
		seconds := int64(time.Minute / time.Second)
		w.Header().Set("Retry-After", strconv.FormatInt((seconds+h.cfg.RatePerMinute-1)/h.cfg.RatePerMinute, 10))
	}

	payload := &status.Status{Code: int32(statusCode), Message: message}
	body, _ := proto.Marshal(payload)

	w.Header().Set("Content-Type", protobufType)

	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		body, _ = protojson.Marshal(payload)

		w.Header().Set("Content-Type", "application/json")
	}

	w.WriteHeader(code)
	_, _ = w.Write(body)
}

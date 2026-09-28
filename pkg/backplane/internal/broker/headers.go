package broker

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
)

// CloudEvents over the NATS binding (binary mode): attributes and
// extensions are ce-* headers, the payload is the data.
const (
	HeaderSpecVersion = "ce-specversion"
	HeaderID          = "ce-id"
	HeaderSource      = "ce-source"
	HeaderType        = "ce-type"
	HeaderTime        = "ce-time"
	HeaderSubject     = "ce-subject"
	HeaderContentType = "ce-datacontenttype"
	HeaderInstance    = "ce-instance"
	HeaderVersion     = "ce-version"
	// HeaderMIME is the binding's content type header.
	HeaderMIME = "content-type"

	// HeaderError and HeaderConsumer are added to a dead letter: the last
	// handler error and the reactor that gave up.
	HeaderError    = "bp-error"
	HeaderConsumer = "bp-consumer"
	// HeaderDelivered: deliveries before the message was dead-lettered.
	HeaderDelivered = "bp-delivered"

	specVersion = "1.0"
	contentJSON = "application/json"
	// natsPrefix marks the server's own headers, never copied to a dead
	// letter.
	natsPrefix = "Nats-"
	// maxError bounds bp-error: headers share the message size limit.
	maxError = 4096
)

// cloudEvent is the metadata of one published event.
type cloudEvent struct {
	ID       string
	Service  string
	Instance string
	Version  string
	Type     string // full event name <service>.<Event>
	Key      string // ce-subject, optional
	Time     time.Time
}

// headers of e, with the trace context of ctx; Nats-Msg-Id = ce-id lets
// JetStream drop a duplicate publish.
func headers(ctx context.Context, e cloudEvent) nats.Header {
	h := nats.Header{}
	h.Set(HeaderSpecVersion, specVersion)
	h.Set(HeaderID, e.ID)
	h.Set(HeaderSource, e.Service)
	h.Set(HeaderType, e.Type)
	h.Set(HeaderTime, e.Time.UTC().Format(time.RFC3339Nano))
	h.Set(HeaderContentType, contentJSON)
	h.Set(HeaderMIME, contentJSON)

	if e.Key != "" {
		h.Set(HeaderSubject, e.Key)
	}

	if e.Instance != "" {
		h.Set(HeaderInstance, e.Instance)
	}

	if e.Version != "" {
		h.Set(HeaderVersion, e.Version)
	}

	h.Set(jetstream.MsgIDHeader, e.ID)
	otel.GetTextMapPropagator().Inject(ctx, carrier(h))

	return h
}

// deadHeaders are the headers of a dead letter: the original ones without
// the server's, plus why and where it failed. id deduplicates retries of
// the same dead letter.
func deadHeaders(orig nats.Header, id, consumer, cause string, delivered uint64) nats.Header {
	h := nats.Header{}

	for k, vs := range orig {
		if strings.HasPrefix(k, natsPrefix) {
			continue
		}

		h[k] = append([]string(nil), vs...)
	}

	if len(cause) > maxError {
		cause = cause[:maxError]
	}

	h.Set(HeaderError, cause)
	h.Set(HeaderConsumer, consumer)
	h.Set(HeaderDelivered, strconv.FormatUint(delivered, 10))
	h.Set(jetstream.MsgIDHeader, id)

	return h
}

// carrier adapts NATS headers to the OpenTelemetry propagator.
type carrier nats.Header

func (c carrier) Get(key string) string { return nats.Header(c).Get(key) }

func (c carrier) Set(key, value string) { nats.Header(c).Set(key, value) }

func (c carrier) Keys() []string {
	out := make([]string, 0, len(c))
	for k := range c {
		out = append(out, k)
	}

	return out
}

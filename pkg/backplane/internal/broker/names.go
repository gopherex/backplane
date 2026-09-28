package broker

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Names of the NATS objects (design §8, §17). Service, event and consumer
// names become NATS name and subject tokens through Token, so any name maps
// to valid, distinct NATS names; the platform's own names ([a-z0-9-]
// services, CamelCase events) pass through unchanged.
const (
	prefix     = "bp"
	dlqToken   = "dlq"
	durableSep = "__"
	// maxName keeps durable names well inside the server's limits; longer
	// ones are cut and suffixed with a hash of the whole.
	maxName  = 200
	hashLen  = 16
	hexUpper = "0123456789ABCDEF"
)

// Errors of names.
var (
	// ErrEventName: an event name is not "<service>.<Event>".
	ErrEventName = errors.New("broker: event name must be <service>.<Event>")
	// ErrReserved: the service name is reserved for dead letters.
	ErrReserved = errors.New("broker: service name dlq is reserved for dead letters")
)

// Token escapes s into one NATS token: ASCII letters, digits and '-' stay,
// every other byte becomes _XX (upper-case hex). The mapping is injective,
// and an escaped token never contains "__".
func Token(s string) string {
	var b strings.Builder

	b.Grow(len(s))

	for i := range len(s) {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' {
			b.WriteByte(c)

			continue
		}

		b.WriteByte('_')
		b.WriteByte(hexUpper[c>>4])
		b.WriteByte(hexUpper[c&0x0f])
	}

	return b.String()
}

// StreamName of service's events: bp_<service>.
func StreamName(service string) string { return prefix + "_" + Token(service) }

// StreamSubjects of service's stream: bp.<service>.>.
func StreamSubjects(service string) string { return prefix + "." + Token(service) + ".>" }

// Subject of service's event name: bp.<service>.<Event>.
func Subject(service, name string) string { return prefix + "." + Token(service) + "." + Token(name) }

// DLQStreamName of the dead letters of subscriber's reactors:
// bp_dlq_<subscriber>.
func DLQStreamName(subscriber string) string {
	return prefix + "_" + dlqToken + "_" + Token(subscriber)
}

// DLQStreamSubjects of subscriber's dead-letter stream: bp.dlq.<subscriber>.>.
func DLQStreamSubjects(subscriber string) string {
	return prefix + "." + dlqToken + "." + Token(subscriber) + ".>"
}

// DLQSubject of the dead letters of one reactor: bp.dlq.<subscriber>.<consumer>.
func DLQSubject(subscriber, consumer string) string {
	return prefix + "." + dlqToken + "." + Token(subscriber) + "." + Token(consumer)
}

// Durable name of subscriber's reactor consumer: <subscriber>__<consumer>,
// both escaped; unique because escaped tokens never contain "__". A name
// longer than maxName is cut and ends with a hash of the full name.
func Durable(subscriber, consumer string) string {
	name := Token(subscriber) + durableSep + Token(consumer)
	if len(name) <= maxName {
		return name
	}

	sum := sha256.Sum256([]byte(name))

	return name[:maxName-hashLen-1] + "_" + hex.EncodeToString(sum[:])[:hashLen]
}

// SplitEvent splits a full event name "<service>.<Event>" at its first dot.
func SplitEvent(full string) (string, string, error) {
	service, name, ok := strings.Cut(full, ".")
	if !ok || service == "" || name == "" {
		return "", "", fmt.Errorf("%w, got %q", ErrEventName, full)
	}

	return service, name, nil
}

// checkService rejects a service whose stream would overlap dead letters.
func checkService(service string) error {
	if Token(service) == dlqToken {
		return ErrReserved
	}

	return nil
}

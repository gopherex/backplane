// Package logsql builds VictoriaLogs LogsQL queries. Values enter only
// through its functions, quoted (and turned into escaped regular
// expressions where a pattern is needed); field names are the caller's
// constants, checked and quoted too. There is no raw LogsQL input.
package logsql

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Filter is a LogsQL filter expression.
type Filter struct{ expr string }

// String is the filter as LogsQL.
func (f Filter) String() string { return f.expr }

// IsZero reports an empty filter (matches nothing is never implied).
func (f Filter) IsZero() bool { return f.expr == "" }

// Field is a stored field name.
type Field string

var fieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)

func (f Field) quoted() string {
	if !fieldName.MatchString(string(f)) {
		panic("logsql: invalid field name " + strconv.Quote(string(f)))
	}

	return strconv.Quote(string(f))
}

// Name is the field as a pipe argument.
func (f Field) Name() string { return f.quoted() }

// All matches every record.
func All() Filter { return Filter{"*"} }

// Eq: the field equals value exactly (the stream id matches by id).
func (f Field) Eq(value string) Filter {
	if f == "_stream_id" {
		return Filter{f.quoted() + ":" + strconv.Quote(value)}
	}

	return Filter{f.quoted() + ":=" + strconv.Quote(value)}
}

// Regexp: the field matches a pattern the caller built from constants.
func (f Field) Regexp(pattern string) Filter {
	return Filter{f.quoted() + ":~" + strconv.Quote(pattern)}
}

// Contains: the field contains value, case-insensitive when fold.
func (f Field) Contains(value string, fold bool) Filter {
	pattern := regexp.QuoteMeta(value)
	if fold {
		pattern = "(?i)" + pattern
	}

	return f.Regexp(pattern)
}

// Prefix: the field starts with value.
func (f Field) Prefix(value string) Filter { return f.Regexp("^" + regexp.QuoteMeta(value)) }

// Set: the field is stored and not empty (LogsQL does not tell them apart).
func (f Field) Set() Filter { return Filter{f.quoted() + ":*"} }

// Range: records in [start, end).
func Range(start, end time.Time) Filter {
	return Filter{"_time:[" + strconv.Quote(start.UTC().Format(time.RFC3339Nano)) + "," +
		strconv.Quote(end.UTC().Format(time.RFC3339Nano)) + ")"}
}

// And holds when every filter holds (empty ones skipped).
func And(filters ...Filter) Filter { return join(" AND ", filters) }

// Or holds when any filter holds (empty ones skipped).
func Or(filters ...Filter) Filter { return join(" OR ", filters) }

// Not negates.
func Not(f Filter) Filter { return Filter{"NOT (" + f.expr + ")"} }

func join(operator string, filters []Filter) Filter {
	parts := make([]string, 0, len(filters))
	for _, f := range filters {
		if !f.IsZero() {
			parts = append(parts, "("+f.expr+")")
		}
	}

	switch len(parts) {
	case 0:
		return Filter{}
	case 1:
		return Filter{strings.TrimSuffix(strings.TrimPrefix(parts[0], "("), ")")}
	default:
		return Filter{strings.Join(parts, operator)}
	}
}

// Query is a filter followed by pipes.
type Query struct {
	filter Filter
	pipes  []string
}

// From starts a query over filter.
func From(filter Filter) Query { return Query{filter: filter} }

// String is the query as LogsQL.
func (q Query) String() string {
	return strings.Join(append([]string{q.filter.expr}, q.pipes...), " | ")
}

func (q Query) pipe(p string) Query {
	q.pipes = append(slices.Clone(q.pipes), p)

	return q
}

// Fields keeps only fields.
func (q Query) Fields(fields ...Field) Query { return q.pipe("fields " + names(fields)) }

// LastBy keeps, per value of partition, the record with the greatest by.
func (q Query) LastBy(by, partition Field) Query {
	return q.pipe("last 1 by (" + by.quoted() + ") partition by (" + partition.quoted() + ")")
}

// Order is one sort key.
type Order struct {
	Field Field
	Desc  bool
}

// Sort orders by keys and keeps the first limit.
func (q Query) Sort(limit int, keys ...Order) Query {
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		part := k.Field.quoted()
		if k.Desc {
			part += " desc"
		}

		parts = append(parts, part)
	}

	return q.pipe("sort by (" + strings.Join(parts, ", ") + ") limit " + strconv.Itoa(limit))
}

// Hash adds as the hash of the whole record (packed as JSON).
func (q Query) Hash(as Field) Query {
	packed := Field(string(as) + "_row")

	return q.pipe("pack_json as " + packed.quoted()).pipe("hash(" + packed.quoted() + ") as " + as.quoted()).
		pipe("delete " + packed.quoted())
}

// Where filters the pipe's rows.
func (q Query) Where(f Filter) Query { return q.pipe("filter " + f.expr) }

// CountByTime counts rows per step of time as hits.
func (q Query) CountByTime(timeField Field, step time.Duration, hits Field) Query {
	return q.pipe("stats by (" + timeField.quoted() + ":" + step.String() + ") count() as " + hits.quoted())
}

// Stat is one aggregate of Stats.
type Stat struct {
	expr string
}

// CountUniqIf counts distinct values of field among rows matching if.
func CountUniqIf(field Field, cond Filter, as Field) Stat {
	return Stat{"count_uniq(" + field.quoted() + ") if (" + cond.expr + ") as " + as.quoted()}
}

// CountIf counts rows matching if.
func CountIf(cond Filter, as Field) Stat {
	return Stat{"count() if (" + cond.expr + ") as " + as.quoted()}
}

// Max is the greatest value of field.
func Max(field, as Field) Stat { return Stat{"max(" + field.quoted() + ") as " + as.quoted()} }

// Stats aggregates per value of by.
func (q Query) Stats(key Field, stats ...Stat) Query {
	parts := make([]string, 0, len(stats))
	for _, s := range stats {
		parts = append(parts, s.expr)
	}

	return q.pipe("stats by (" + key.quoted() + ") " + strings.Join(parts, ", "))
}

var plainName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Sum sets as to a + b; math takes plain names (aliases of Stats).
func (q Query) Sum(a, b, alias Field) Query {
	for _, f := range []Field{a, b, alias} {
		if !plainName.MatchString(string(f)) {
			panic("logsql: math takes plain names, not " + strconv.Quote(string(f)))
		}
	}

	return q.pipe("math " + string(a) + " + " + string(b) + " as " + string(alias))
}

func names(fields []Field) string {
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		parts = append(parts, f.quoted())
	}

	return strings.Join(parts, ", ")
}

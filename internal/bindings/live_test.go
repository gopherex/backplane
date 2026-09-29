package bindings_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	consolev1 "github.com/gopherex/backplane/backplanepb/console/v1"
	backplanev1 "github.com/gopherex/backplane/backplanepb/v1"
	"github.com/gopherex/backplane/internal/bindings"
	"github.com/gopherex/backplane/internal/registry"
	"github.com/gopherex/backplane/internal/store"
	"github.com/gopherex/backplane/pkg/backplane/backplanetest"
	sdkconfig "github.com/gopherex/backplane/pkg/backplane/config"
	"github.com/gopherex/backplane/pkg/backplane/deps"
)

// live is the Manager over PostgreSQL (BACKPLANE_TEST_PG, skipped
// without) and a registry Hub holding the fixture manifests, iam renamed
// to a random service so runs do not see each other's versions.
func live(t *testing.T) (*bindings.Manager, string) {
	t.Helper()

	dsn := os.Getenv("BACKPLANE_TEST_PG")
	if dsn == "" {
		t.Skip("BACKPLANE_TEST_PG not set")
	}

	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}

	iam := "iam-" + hex.EncodeToString(b)
	services := map[string]registry.Service{}

	for _, m := range manifests(t) {
		if m.GetService() == "iam" {
			m = proto.CloneOf(m)
			m.Service = iam
		}

		services[m.GetService()] = registry.Service{
			Name: m.GetService(), Manifests: map[string]*backplanev1.Manifest{m.GetVersion(): m},
		}
	}

	hub := registry.NewHub()
	hub.Publish(services)

	h := backplanetest.New(t, backplanetest.Name("backplane"))
	st := deps.NewDependency(h.Root(), store.New(sdkconfig.Secret(dsn)))
	m := bindings.New(h.Root(), st, hub, bindings.PollInterval(100*time.Millisecond))

	h.Start()

	if err := backplanetest.Ready(h); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		pool := st.Get().Pool
		_, _ = pool.Exec(ctx, "DELETE FROM backplane.binding_current WHERE hook LIKE $1", iam+".%")
		_, _ = pool.Exec(ctx, "DELETE FROM backplane.binding_version WHERE hook LIKE $1", iam+".%")

		var ids []uuid.UUID

		rows, err := pool.Query(ctx,
			"SELECT DISTINCT rule_id FROM backplane.rule_version WHERE definition->>'event' LIKE $1", iam+".%")
		if err == nil {
			for rows.Next() {
				var id uuid.UUID
				if rows.Scan(&id) == nil {
					ids = append(ids, id)
				}
			}

			rows.Close()
		}

		for _, q := range []string{
			"DELETE FROM backplane.rule_current WHERE rule_id = ANY($1)",
			"DELETE FROM backplane.rule_version WHERE rule_id = ANY($1)",
			"DELETE FROM backplane.rule WHERE id = ANY($1)",
		} {
			_, _ = pool.Exec(ctx, q, ids)
		}
	})

	return m, iam
}

func code(err error) codes.Code { return status.Code(err) }

// The life of a binding through BindingService: rejected, saved, paged,
// rolled back, deleted; the hook's state follows.
//
//nolint:paralleltest // one database
func TestBindingLifecycle(t *testing.T) {
	m, iam := live(t)
	ctx := t.Context()
	api := m.BindingAPI()
	hook := iam + ".SendEmail"

	changes := m.Changes(ctx)

	list, err := api.ListBindings(ctx, &consolev1.ListBindingsRequest{Service: iam})
	if err != nil {
		t.Fatal(err)
	}

	if len(list.GetBindings()) != 2 || list.GetBindings()[1].GetHook() != hook ||
		list.GetBindings()[1].GetState() != consolev1.BindingState_BINDING_STATE_REQUIRED_UNBOUND ||
		list.GetBindings()[0].GetState() != consolev1.BindingState_BINDING_STATE_UNBOUND {
		t.Fatalf("list before: %v", list)
	}

	parsed, err := api.ParseBinding(ctx, &consolev1.ParseBindingRequest{
		Text: hook + " :=\n  send = smtp.Send(to: req.to, subject: \"s\", text: req.template)\n  return { message_id: send.id }\n",
	})
	if err != nil || len(parsed.GetErrors()) > 0 {
		t.Fatalf("parse: %v %v", parsed, err)
	}

	def := parsed.GetDefinition()

	bad := proto.CloneOf(def)
	bad.Steps[0].Activity = "smtp.Nope"

	rejected, err := api.SaveBinding(ctx, &consolev1.SaveBindingRequest{Definition: bad})
	if err != nil || rejected.GetVersion() != nil || len(rejected.GetViolations()) == 0 ||
		rejected.GetViolations()[0].GetCode() != bindings.CodeUnknownActivity {
		t.Fatalf("rejected: %v %v", rejected, err)
	}

	v1 := save(t, api, def, "first")
	if v1.GetVersion() != 1 || v1.GetAuthor() != bindings.DefaultAuthor || v1.GetComment() != "first" {
		t.Fatalf("v1: %v", v1)
	}

	select {
	case <-changes:
	case <-time.After(5 * time.Second):
		t.Fatal("no change after a save")
	}

	b, version, err := m.Active(ctx, hook)
	if err != nil || version != 1 || !b.Equal(bindings.BindingFromPB(def)) {
		t.Fatalf("active: %+v %d %v", b, version, err)
	}

	changed := proto.CloneOf(def)
	changed.Steps[0].When = "req.to != \"\""
	save(t, api, changed, "second")

	page, err := api.ListBindingVersions(ctx, &consolev1.ListBindingVersionsRequest{Hook: hook, PageSize: 1})
	if err != nil || len(page.GetVersions()) != 1 || page.GetVersions()[0].GetVersion() != 2 || page.GetNextBefore() != 2 {
		t.Fatalf("page 1: %v %v", page, err)
	}

	page, err = api.ListBindingVersions(ctx, &consolev1.ListBindingVersionsRequest{Hook: hook, Before: 2, PageSize: 1})
	if err != nil || len(page.GetVersions()) != 1 || page.GetVersions()[0].GetVersion() != 1 || page.GetNextBefore() != 0 {
		t.Fatalf("page 2: %v %v", page, err)
	}

	rb, err := api.RollbackBinding(ctx, &consolev1.RollbackBindingRequest{Hook: hook, Version: 1, Comment: "back"})
	if err != nil || rb.GetVersion().GetVersion() != 3 || rb.GetVersion().GetRollbackOf() != 1 ||
		!proto.Equal(rb.GetVersion().GetDefinition(), def) {
		t.Fatalf("rollback: %v %v", rb, err)
	}

	del, err := api.DeleteBinding(ctx, &consolev1.DeleteBindingRequest{Hook: hook, Comment: "gone"})
	if err != nil || del.GetVersion().GetVersion() != 4 || !del.GetVersion().GetDeleted() || del.GetVersion().GetDefinition() != nil {
		t.Fatalf("delete: %v %v", del, err)
	}

	if _, _, err := m.Active(ctx, hook); !errors.Is(err, bindings.ErrNoBinding) {
		t.Fatalf("active after delete: %v", err)
	}

	if _, err := api.DeleteBinding(ctx, &consolev1.DeleteBindingRequest{Hook: hook}); code(err) != codes.NotFound {
		t.Fatalf("delete again: %v", err)
	}

	if _, err := api.GetBinding(ctx, &consolev1.GetBindingRequest{Hook: hook, Version: 99}); code(err) != codes.NotFound {
		t.Fatalf("get of a missing version: %v", err)
	}

	got, err := api.GetBinding(ctx, &consolev1.GetBindingRequest{Hook: hook, Version: 2})
	if err != nil || !proto.Equal(got.GetVersion().GetDefinition(), changed) {
		t.Fatalf("get v2: %v %v", got, err)
	}

	list, err = api.ListBindings(ctx, &consolev1.ListBindingsRequest{Service: iam})
	if err != nil || list.GetBindings()[1].GetState() != consolev1.BindingState_BINDING_STATE_REQUIRED_UNBOUND ||
		!list.GetBindings()[1].GetCurrent().GetDeleted() {
		t.Fatalf("list after delete: %v %v", list, err)
	}

	text, err := api.FormatBinding(ctx, &consolev1.FormatBindingRequest{Definition: def})
	if err != nil {
		t.Fatal(err)
	}

	again, err := api.ParseBinding(ctx, &consolev1.ParseBindingRequest{Text: text.GetText()})
	if err != nil || !proto.Equal(again.GetDefinition(), def) {
		t.Fatalf("format and parse: %v %v", again, err)
	}
}

func save(t *testing.T, api bindings.BindingAPI, def *consolev1.BindingDefinition, comment string) *consolev1.BindingVersion {
	t.Helper()

	out, err := api.SaveBinding(t.Context(), &consolev1.SaveBindingRequest{Definition: def, Comment: comment})
	if err != nil || len(out.GetViolations()) > 0 {
		t.Fatalf("save: %v %v", out, err)
	}

	return out.GetVersion()
}

// WatchBindings sends the list now and after a save.
//
//nolint:paralleltest // one database
func TestWatchBindings(t *testing.T) {
	m, iam := live(t)
	ctx, cancel := context.WithCancel(t.Context())

	defer cancel()

	stream := &fakeStream[consolev1.WatchBindingsResponse]{ctx: ctx, ch: make(chan *consolev1.WatchBindingsResponse, 8)}
	done := make(chan error, 1)

	go func() { done <- m.BindingAPI().WatchBindings(&consolev1.WatchBindingsRequest{Service: iam}, stream) }()

	first := stream.next(t)
	if len(first.GetBindings()) != 2 || first.GetBindings()[1].GetCurrent() != nil {
		t.Fatalf("first: %v", first)
	}

	b := mustParse(t, iam+".Audit :=\n  x = billing.Charge(v: 1)\n")
	if _, err := m.Save(ctx, b, ""); err != nil {
		t.Fatal(err)
	}

	for {
		next := stream.next(t)
		if next.GetBindings()[0].GetState() == consolev1.BindingState_BINDING_STATE_BOUND {
			break
		}
	}

	cancel()

	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type fakeStream[T any] struct {
	grpc.ServerStream

	ctx context.Context
	ch  chan *T
}

func (s *fakeStream[T]) Context() context.Context { return s.ctx }

func (s *fakeStream[T]) Send(m *T) error {
	s.ch <- m

	return nil
}

func (s *fakeStream[T]) next(t *testing.T) *T {
	t.Helper()

	select {
	case m := <-s.ch:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("no message")

		return nil
	}
}

// The life of a rule through RuleService: created, listed by event,
// paused, resumed, rolled back, deleted.
//
//nolint:paralleltest // one database
func TestRuleLifecycle(t *testing.T) {
	m, iam := live(t)
	ctx := t.Context()
	api := m.RuleAPI()
	event := iam + ".UserRegistered"

	parsed, err := api.ParseRule(ctx, &consolev1.ParseRuleRequest{
		Text: "on " + event + " when event.email != \"\" :=\n  send = smtp.Send(to: event.email, subject: \"hi\", text: event.name)\n",
	})
	if err != nil || len(parsed.GetErrors()) > 0 {
		t.Fatalf("parse: %v %v", parsed, err)
	}

	def := parsed.GetDefinition()

	noName, err := api.SaveRule(ctx, &consolev1.SaveRuleRequest{Definition: def})
	if err != nil || len(noName.GetViolations()) == 0 || noName.GetViolations()[0].GetPath() != "name" {
		t.Fatalf("without a name: %v %v", noName, err)
	}

	created, err := api.SaveRule(ctx, &consolev1.SaveRuleRequest{Name: "welcome", Definition: def, Comment: "c"})
	if err != nil || created.GetVersion().GetVersion() != 1 || created.GetVersion().GetName() != "welcome" {
		t.Fatalf("create: %v %v", created, err)
	}

	id := created.GetVersion().GetRuleId()

	listed, err := api.ListRules(ctx, &consolev1.ListRulesRequest{Event: event})
	if err != nil || len(listed.GetRules()) != 1 || listed.GetRules()[0].GetId() != id {
		t.Fatalf("list: %v %v", listed, err)
	}

	renamed, err := api.SaveRule(ctx, &consolev1.SaveRuleRequest{Id: id, Name: "welcome2", Definition: def})
	if err != nil || renamed.GetVersion().GetVersion() != 2 || renamed.GetVersion().GetRuleId() != id {
		t.Fatalf("save v2: %v %v", renamed, err)
	}

	paused, err := api.PauseRule(ctx, &consolev1.PauseRuleRequest{Id: id})
	if err != nil || !paused.GetRule().GetPaused() {
		t.Fatalf("pause: %v %v", paused, err)
	}

	if active := activeIDs(t, m); active[id] {
		t.Fatal("a paused rule is active")
	}

	if resumed, err := api.ResumeRule(ctx, &consolev1.ResumeRuleRequest{Id: id}); err != nil || resumed.GetRule().GetPaused() {
		t.Fatalf("resume: %v %v", resumed, err)
	}

	if active := activeIDs(t, m); !active[id] {
		t.Fatal("a resumed rule is not active")
	}

	rb, err := api.RollbackRule(ctx, &consolev1.RollbackRuleRequest{Id: id, Version: 1})
	if err != nil || rb.GetVersion().GetVersion() != 3 || rb.GetVersion().GetName() != "welcome" ||
		rb.GetVersion().GetRollbackOf() != 1 {
		t.Fatalf("rollback: %v %v", rb, err)
	}

	got, err := api.GetRule(ctx, &consolev1.GetRuleRequest{Id: id, Version: 2})
	if err != nil || got.GetVersion().GetName() != "welcome2" || got.GetRule().GetCurrent().GetVersion() != 3 {
		t.Fatalf("get v2: %v %v", got, err)
	}

	if _, err := api.DeleteRule(ctx, &consolev1.DeleteRuleRequest{Id: id}); err != nil {
		t.Fatal(err)
	}

	if _, err := api.DeleteRule(ctx, &consolev1.DeleteRuleRequest{Id: id}); code(err) != codes.FailedPrecondition {
		t.Fatalf("delete again: %v", err)
	}

	if active := activeIDs(t, m); active[id] {
		t.Fatal("a deleted rule is active")
	}

	versions, err := api.ListRuleVersions(ctx, &consolev1.ListRuleVersionsRequest{Id: id})
	if err != nil || len(versions.GetVersions()) != 4 || !versions.GetVersions()[0].GetDeleted() {
		t.Fatalf("versions: %v %v", versions, err)
	}

	if _, err := api.GetRule(ctx, &consolev1.GetRuleRequest{Id: uuid.NewString()}); code(err) != codes.NotFound {
		t.Fatalf("get of an unknown rule: %v", err)
	}

	if _, err := api.GetRule(ctx, &consolev1.GetRuleRequest{Id: "nope"}); code(err) != codes.InvalidArgument {
		t.Fatalf("get with a bad id: %v", err)
	}
}

func activeIDs(t *testing.T, m *bindings.Manager) map[string]bool {
	t.Helper()

	rules, err := m.ActiveRules(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	out := map[string]bool{}
	for i := range rules {
		out[rules[i].ID.String()] = true
	}

	return out
}

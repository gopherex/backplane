package route_test

import (
	"bytes"
	"errors"
	"testing"
	"testing/fstest"

	"google.golang.org/protobuf/proto"

	"github.com/gopherex/backplane/pkg/backplane/internal/apifiles"
	"github.com/gopherex/backplane/pkg/backplane/internal/link"
	"github.com/gopherex/backplane/pkg/backplane/internal/manifest"
	"github.com/gopherex/backplane/pkg/backplane/route"
)

func TestLargeAPIStaysOutsideManifest(t *testing.T) {
	t.Parallel()

	files := fstest.MapFS{"api.yaml": {Data: bytes.Repeat([]byte("x"), manifest.MaxSize+1)}}
	declaration := route.HTTP("/api/", route.OpenAPIFS(files, "api.yaml"))

	r, err := link.Routes.Decl(declaration)
	if err != nil {
		t.Fatal(err)
	}

	builder := manifest.New("hello", "1.0.0")
	builder.Route(r)

	m, err := builder.Build()
	if err != nil || proto.Size(m) > 1024 || r.GetBundle().GetHash() == "" {
		t.Fatalf("manifest: %v %v", m, err)
	}

	if link.Routes.Files(declaration) == nil {
		t.Fatal("file snapshot not attached")
	}
}

func TestAPIFileDeclarationErrors(t *testing.T) {
	t.Parallel()

	d := route.HTTP("/api/", route.OpenAPIFS(fstest.MapFS{}, "missing.yaml"))
	if _, err := link.Routes.Decl(d); !errors.Is(err, apifiles.ErrBundle) {
		t.Fatalf("missing entry: %v", err)
	}

	d = route.HTTP("/api/", route.OpenAPIFS(fstest.MapFS{}, "missing.yaml"), route.OpenAPI([]byte("{}")))
	if r, err := link.Routes.Decl(d); err != nil || r.GetBundle() != nil || link.Routes.Files(d) != nil {
		t.Fatalf("last option did not replace files: %v %v", r, err)
	}
}

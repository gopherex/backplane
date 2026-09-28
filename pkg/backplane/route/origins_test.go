package route_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gopherex/backplane/pkg/backplane/route"
)

func TestOrigins(t *testing.T) {
	t.Parallel()

	req := func(origin string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://api.example.com/ws", http.NoBody)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}

		return r
	}

	cases := []struct {
		name    string
		origins route.Origins
		origin  string
		want    bool
	}{
		{"no origin header", route.Origins{}, "", true},
		{"same origin", route.Origins{}, "https://api.example.com", true},
		{"zero value cross origin", route.Origins{}, "https://evil.test", false},
		{"pattern", route.AllowOrigins("*.example.com"), "https://app.example.com", true},
		{"pattern miss", route.AllowOrigins("*.example.com"), "https://evil.test", false},
		{"any", route.AnyOrigin(), "https://evil.test", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			if got := c.origins.Allow(req(c.origin)); got != c.want {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}

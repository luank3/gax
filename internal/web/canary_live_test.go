//go:build webcanary

package web_test

import (
	"context"
	"testing"

	"github.com/luank3/gax/internal/web"
)

func TestSearchCanaryLive(t *testing.T) {
	for _, provider := range []string{web.BackendYou, web.BackendDuckDuckGo} {
		t.Run(provider, func(t *testing.T) {
			if err := runSearchCanary(context.Background(), web.NewClient(), provider); err != nil {
				t.Fatal(err)
			}
			t.Logf("%s returned usable results", provider)
		})
	}
}

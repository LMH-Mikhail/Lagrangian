package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/lagrangian-dev/lagrangian/internal/core"
	"github.com/lagrangian-dev/lagrangian/internal/platform/paths"
	"github.com/lagrangian-dev/lagrangian/internal/platform/secrets"
)

func newWizardService(t *testing.T) *core.Service {
	t.Helper()
	layout, err := paths.Resolve(t.TempDir())
	if err != nil {
		t.Fatalf("paths.Resolve: %v", err)
	}
	sec, err := secrets.NewFileStore(layout.SecretsDir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	svc, err := core.NewService(layout, layout.ConfigFile, sec)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

func TestRunInitWizardServerModeDefaultsAndTokenGeneration(t *testing.T) {
	svc := newWizardService(t)
	// Mode=2 (server), default port 19000, default dataDir,
	// default auth=Y, blank token (auto-generate), confirm=Y
	input := strings.NewReader("2\n\n\n\n\ny\n")
	var out bytes.Buffer

	res, err := RunInitWizard(svc, input, &out, false)
	if err != nil {
		t.Fatalf("RunInitWizard: %v\noutput:\n%s", err, out.String())
	}
	if res.Config.Server.Mode != "server" || res.Config.Server.EffectiveHost() != "0.0.0.0" {
		t.Fatalf("unexpected server config: %+v", res.Config.Server)
	}
	if !res.Config.Server.AuthEnabled || !strings.HasPrefix(res.GeneratedToken, "lg_") {
		t.Fatalf("expected auto-generated token, got auth=%v token=%q", res.Config.Server.AuthEnabled, res.GeneratedToken)
	}
	if !svc.VerifyToken(res.GeneratedToken) {
		t.Fatalf("service failed to verify generated token")
	}
	if !strings.Contains(out.String(), res.GeneratedToken) {
		t.Fatalf("expected wizard output to display generated token once")
	}
}

func TestRunInitWizardStandaloneAndCancel(t *testing.T) {
	svc := newWizardService(t)
	// Cancel at confirmation step: mode=1, port=default, dataDir=default, auth=default, confirm=n
	cancelIn := strings.NewReader("1\n\n\n\nn\n")
	var out bytes.Buffer
	if _, err := RunInitWizard(svc, cancelIn, &out, false); !errors.Is(err, ErrCanceled) {
		t.Fatalf("expected ErrCanceled, got %v", err)
	}
	if svc.IsInitialized() {
		t.Fatalf("service should remain uninitialized after cancel")
	}

	// Complete standalone with defaults: mode=default, port=default, dataDir=default, auth=default, confirm=default(Y)
	okIn := strings.NewReader("\n\n\n\n\n")
	res, err := RunInitWizard(svc, okIn, &out, false)
	if err != nil {
		t.Fatalf("standalone init failed: %v", err)
	}
	if res.Config.Server.Mode != "standalone" || res.Config.Server.EffectiveHost() != "127.0.0.1" || res.Config.Server.AuthEnabled {
		t.Fatalf("unexpected standalone config: %+v", res.Config.Server)
	}
}

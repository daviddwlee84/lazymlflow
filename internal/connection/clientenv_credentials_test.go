package connection

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazymlflow/internal/core"
)

func TestSDKCredentialFileBasicPair(t *testing.T) {
	for _, test := range []struct {
		name, content string
		pair, invalid bool
	}{
		{"empty", "", false, false},
		{"other section", "[other]\nmlflow_tracking_username = stored-user\nmlflow_tracking_password = stored-secret", false, false},
		{"pair", "[mlflow]\nMLFLOW_TRACKING_USERNAME: stored-user\nmlflow_tracking_password = stored-secret", true, false},
		{"partial", "[mlflow]\nmlflow_tracking_username = stored-user\n", false, false},
		{"empty password", "[mlflow]\nmlflow_tracking_username=stored-user\nmlflow_tracking_password=\n", false, false},
		{"defaults", "[DEFAULT]\nuser=stored-user\nmlflow_tracking_password=stored%%secret\n[mlflow]\nmlflow_tracking_username=%(user)s\n", true, false},
		{"explicit empty overrides default", "[DEFAULT]\nmlflow_tracking_username=user\nmlflow_tracking_password=secret\n[mlflow]\nmlflow_tracking_password=\n", false, false},
		{"empty interpolation", "[mlflow]\nuser=\nmlflow_tracking_username=%(user)s\nmlflow_tracking_password=secret\n", false, false},
		{"continuation", "[mlflow]\nmlflow_tracking_username=user\nmlflow_tracking_password=\n  continued-password\n", true, false},
		{"invalid interpolation", "[mlflow]\nmlflow_tracking_username=%(missing)s\nmlflow_tracking_password=secret\n", false, true},
		{"cycle", "[mlflow]\nmlflow_tracking_username=%(mlflow_tracking_username)s\nmlflow_tracking_password=secret\n", false, true},
		{"invalid file", "password-secret-without-a-section", false, true},
		{"duplicate option", "[mlflow]\nmlflow_tracking_username=one\nmlflow_tracking_username=two\n", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			pair, err := sdkFileBasicPair(test.content)
			if (err != nil) != test.invalid || (!test.invalid && pair != test.pair) {
				t.Fatalf("pair=%v error=%v", pair, err)
			}
		})
	}
}

func TestClientCredentialFileConflictIsBoundedAndDoesNotDiscloseValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials")
	if err := checkClientCredentialFileAt(path); err != nil {
		t.Fatal(err)
	}
	for _, contents := range []string{
		"[mlflow]\nmlflow_tracking_username=stored-user\nmlflow_tracking_password=stored-secret\n",
		"malformed-stored-secret",
		strings.Repeat("oversized-secret", 6000),
	} {
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		err := checkClientCredentialFileAt(path)
		if err == nil || !strings.Contains(err.Error(), "~/.mlflow/credentials") {
			t.Fatalf("missing conflict: %v", err)
		}
		for _, secret := range []string{"stored-user", "stored-secret", "oversized-secret"} {
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("credential value disclosed in error")
			}
		}
	}
	for _, target := range []core.Target{{Transient: true}, {UsernameEnv: "USERNAME", PasswordEnv: "PASSWORD"}} {
		if err := CheckClientCredentialFile(target); err != nil {
			t.Fatalf("explicit Basic/transient inspected a default credential file: %v", err)
		}
	}
}

func TestClientCredentialFileSSHRequiresResolvedProxyTokenForException(t *testing.T) {
	// Never inspect the developer's SDK file in this regression.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	directory := filepath.Join(home, ".mlflow")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "credentials"), []byte("[mlflow]\nmlflow_tracking_username=other\nmlflow_tracking_password=other-password\n"), 0600); err != nil {
		t.Fatal(err)
	}
	target := core.Target{ID: "ssh", TrackingURI: "https://example.com", SSHHost: "alias", TokenEnv: "PROXY_TOKEN"}
	t.Setenv("PROXY_TOKEN", "selected-token")
	if err := CheckClientCredentialFile(target); err != nil {
		t.Fatalf("owned proxy token cannot be overridden by SDK Basic: %v", err)
	}
	direct := target
	direct.SSHHost = ""
	if err := CheckClientCredentialFile(direct); err == nil {
		t.Fatal("direct token target did not detect SDK Basic precedence")
	}
	t.Setenv("PROXY_TOKEN", "")
	if err := CheckClientCredentialFile(target); err == nil {
		t.Fatal("empty proxy token allowed SDK Basic passthrough")
	}
	target.TokenEnv = ""
	if err := CheckClientCredentialFile(target); err == nil {
		t.Fatal("unauthenticated SSH proxy allowed SDK Basic passthrough")
	}
}

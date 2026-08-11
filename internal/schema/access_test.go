package schema_test

import (
	"strings"
	"testing"

	"github.com/ownbase/ownbase/internal/schema"
)

func TestOwnbaseAccess_Valid(t *testing.T) {
	cfg := &schema.OwnbaseConfig{
		SchemaVersion: "v1",
		Services: map[string]schema.ServiceDecl{
			"web": {
				Repo: "https://github.com/example/web.git",
				OwnbaseAccess: []string{
					"status:read",
					"service:web:deploy",
					"secrets:web:write",
					"*",
				},
			},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestOwnbaseAccess_Invalid(t *testing.T) {
	cases := []struct {
		name   string
		scopes []string
		want   string
	}{
		{"empty", []string{""}, "empty"},
		{"duplicate", []string{"status:read", "status:read"}, "duplicate"},
		{"bad chars", []string{"status read"}, "invalid"},
		{"bare star suffix", []string{"foo*"}, "invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &schema.OwnbaseConfig{
				SchemaVersion: "v1",
				Services: map[string]schema.ServiceDecl{
					"web": {
						Repo:          "https://github.com/example/web.git",
						OwnbaseAccess: tc.scopes,
					},
				},
			}
			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

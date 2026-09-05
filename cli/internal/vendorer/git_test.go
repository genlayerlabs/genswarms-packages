package vendorer

import (
	"context"
	"strings"
	"testing"
)

func TestGitTransportHasIndependentConfiguration(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_SSH_COMMAND", "fixture-command")
	cmd := gitCommand(context.Background(), t.TempDir(), "https://github.com/fixture/repo.git", "v1")
	args := strings.Join(cmd.Args, " ")
	for _, required := range []string{"protocol.allow=never", "protocol.https.allow=always", "http.followRedirects=false", "--branch v1 -- https://github.com/fixture/repo.git"} {
		if !strings.Contains(args, required) {
			t.Fatalf("missing argument %s", required)
		}
	}
	for _, value := range cmd.Env {
		if strings.HasPrefix(value, "GIT_CONFIG_COUNT=") || strings.HasPrefix(value, "GIT_SSH_COMMAND=") {
			t.Fatal("inherited unsafe git configuration")
		}
	}
}

func TestGitSourceRejectsAuthorityAndOptionInjection(t *testing.T) {
	for _, source := range []string{"github://user@evil/repo@v1", "github://fixture/repo@--upload-pack=x", "github://fixture/repo@../bad", "github://fixture/repo@v1\ncommand"} {
		_, cleanup, err := checkout(source, Options{})
		if cleanup != nil {
			cleanup()
		}
		if err == nil {
			t.Fatalf("accepted invalid source %q", source)
		}
	}
}

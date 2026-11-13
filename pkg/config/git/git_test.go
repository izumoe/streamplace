package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/require"
)

func TestGitCommandHelper(t *testing.T) {
	if os.Getenv("STREAMPLACE_GIT_TEST_HELPER") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append(os.Args[:1], os.Args[i+1:]...)
			break
		}
	}
	flag.CommandLine = flag.NewFlagSet("git-metadata", flag.ContinueOnError)
	if err := makeGit(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func gitTestRepository(t *testing.T) (string, *git.Repository, plumbing.Hash) {
	t.Helper()
	dir := t.TempDir()
	r, err := git.PlainInit(dir, false)
	require.NoError(t, err)
	w, err := r.Worktree()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "source"), []byte("first"), 0600))
	_, err = w.Add("source")
	require.NoError(t, err)
	hash, err := w.Commit("first", &git.CommitOptions{Author: &object.Signature{Name: "test", Email: "test@example.com", When: time.Unix(1700000000, 0)}})
	require.NoError(t, err)
	return dir, r, hash
}

func runGitMetadata(t *testing.T, dir string, env map[string]string, args ...string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestGitCommandHelper$", "--"}, args...)...)
	cmd.Dir = dir
	for _, value := range os.Environ() {
		name := strings.SplitN(value, "=", 2)[0]
		if strings.HasPrefix(name, "GITHUB_") || strings.HasPrefix(name, "CI_") || name == "CF_PAGES" || name == "STREAMPLACE_VERSION_OVERRIDE" {
			continue
		}
		cmd.Env = append(cmd.Env, value)
	}
	cmd.Env = append(cmd.Env, "STREAMPLACE_GIT_TEST_HELPER=1")
	for name, value := range env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	return strings.TrimSpace(string(out))
}

func TestGitMetadataUsesGitHubAndLocalReferences(t *testing.T) {
	dir, r, hash := gitTestRepository(t)
	_, err := r.CreateTag("v1.2.3", hash, nil)
	require.NoError(t, err)
	localVersion := "v1.2.3-" + hash.String()[:8]
	require.Equal(t, localVersion, runGitMetadata(t, dir, nil, "-v"), "local builds retain their existing version suffix")
	head, err := r.Head()
	require.NoError(t, err)
	require.Equal(t, head.Name().Short(), runGitMetadata(t, dir, nil, "-branch"))
	require.Equal(t, "feature-builds", runGitMetadata(t, dir, map[string]string{"GITHUB_REF_TYPE": "branch", "GITHUB_REF_NAME": "feature/builds"}, "-branch"))
	require.Equal(t, "feature-pr", runGitMetadata(t, dir, map[string]string{"GITHUB_HEAD_REF": "feature/pr", "GITHUB_REF_NAME": "123/merge", "GITHUB_REF_TYPE": "branch"}, "-branch"))
	tagEnv := map[string]string{"GITHUB_REF_TYPE": "tag", "GITHUB_REF_NAME": "v1.2.3"}
	require.Equal(t, "v1.2.3", runGitMetadata(t, dir, tagEnv, "-v"))
	require.Equal(t, "latest", runGitMetadata(t, dir, tagEnv, "-branch"))
	require.Equal(t, localVersion, runGitMetadata(t, dir, map[string]string{"GITHUB_REF_TYPE": "tag", "GITHUB_REF_NAME": "v9.9.9"}, "-v"), "an environment tag must actually identify HEAD")
	tagEnv["STREAMPLACE_VERSION_OVERRIDE"] = "custom-version"
	require.Equal(t, "custom-version", runGitMetadata(t, dir, tagEnv, "-v"))
}

func TestGitMetadataHandlesUntaggedAndDetachedCheckouts(t *testing.T) {
	dir, r, hash := gitTestRepository(t)
	require.Equal(t, hash.String()[:8], runGitMetadata(t, dir, nil, "-v"))
	require.NoError(t, r.Storer.SetReference(plumbing.NewHashReference(plumbing.HEAD, hash)))
	require.Equal(t, hash.String()[:8], runGitMetadata(t, dir, nil, "-branch"))
}

func TestGitMetadataUsesAnnotatedGitHubTagOnlyAtHead(t *testing.T) {
	dir, r, hash := gitTestRepository(t)
	_, err := r.CreateTag("v2.0.0", hash, &git.CreateTagOptions{Tagger: &object.Signature{Name: "test", Email: "test@example.com", When: time.Unix(1700000000, 0)}, Message: "release"})
	require.NoError(t, err)
	env := map[string]string{"GITHUB_REF_TYPE": "tag", "GITHUB_REF_NAME": "v2.0.0"}
	require.Equal(t, "v2.0.0", runGitMetadata(t, dir, env, "-v"))
	w, err := r.Worktree()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "source"), []byte("second"), 0600))
	_, err = w.Add("source")
	require.NoError(t, err)
	next, err := w.Commit("second", &git.CommitOptions{Author: &object.Signature{Name: "test", Email: "test@example.com", When: time.Unix(1700000001, 0)}})
	require.NoError(t, err)
	require.Equal(t, "v2.0.0-"+next.String()[:8], runGitMetadata(t, dir, env, "-v"), "a stale CI tag cannot label a later commit as the release")
}

func TestGitMetadataReleaseAssetsUseConfiguredGitHubRepository(t *testing.T) {
	dir, r, hash := gitTestRepository(t)
	_, err := r.CreateTag("v1.2.3", hash, nil)
	require.NoError(t, err)
	for _, repo := range []string{"", "example/fork"} {
		t.Run(repo, func(t *testing.T) {
			env := map[string]string{"GITHUB_REF_TYPE": "tag", "GITHUB_REF_NAME": "v1.2.3", "GITHUB_REPOSITORY": repo}
			if repo == "" {
				repo = "izumoe/streamplace"
			}
			base := "https://github.com/" + repo + "/releases/download/v1.2.3"
			out := runGitMetadata(t, dir, env, "-env")
			require.Contains(t, out, "STREAMPLACE_BRANCH=latest")
			require.Contains(t, out, "STREAMPLACE_VERSION=v1.2.3")
			for _, osName := range []string{"linux", "darwin"} {
				for _, arch := range []string{"amd64", "arm64"} {
					filename := "streamplace-v1.2.3-" + osName + "-" + arch + ".tar.gz"
					require.Contains(t, out, "STREAMPLACE_URL_"+strings.ToUpper(osName+"_"+arch)+"="+base+"/"+filename)
				}
			}
		})
	}
}

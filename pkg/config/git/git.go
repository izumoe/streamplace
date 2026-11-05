package main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/google/uuid"
)

func main() {
	err := makeGit()
	if err != nil {
		panic(err)
	}
}

var tmpl = `package main

var Version = "%s"
var BuildTime = "%d"
var UUID = "%s"
`

var tmplJS = `
export const version = "%s";
export const buildTime = "%d";
export const uuid = "%s";
`

func makeGit() error {
	output := flag.String("o", "", "file to output to")
	version := flag.Bool("v", false, "just print version")
	env := flag.Bool("env", false, "print a bunch of useful environment variables")
	doBranch := flag.Bool("branch", false, "print branch")
	javascript := flag.Bool("js", false, "print code in javascript format")

	flag.Parse()

	// handle CF_PAGES environment fallback
	if os.Getenv("CF_PAGES") != "" && *javascript {
		out := `export const version = "unknown"; export const buildTime = 0; export const uuid = "00000000-0000-0000-0000-000000000000";`
		if *output != "" {
			if err := os.WriteFile(*output, []byte(out), 0644); err != nil {
				return err
			}
		} else {
			fmt.Print(out)
		}
		return nil
	}
	g, err := PlainOpen(".")
	if err != nil {
		return err
	}

	// ... retrieving the HEAD reference
	ref, err := g.Head()
	if err != nil {
		return err
	}
	c, err := g.CommitObject(ref.Hash())
	if err != nil {
		return err
	}

	ts := c.Author.When.Unix()
	rander := rand.New(rand.NewSource(ts))
	u, err := uuid.NewV7FromReader(rander)
	if err != nil {
		return err
	}
	desc, err := g.Describe(ref)
	if err != nil {
		return err
	}
	var out string
	if *version {
		out = desc
	} else if *doBranch {
		out = branch(ref)
	} else if *env {
		StreamplaceBranch := branch(ref)
		outMap := map[string]string{}
		outMap["STREAMPLACE_BRANCH"] = StreamplaceBranch
		outMap["STREAMPLACE_VERSION"] = desc
		repository := os.Getenv("GITHUB_REPOSITORY")
		if repository == "" {
			repository = "izumoe/streamplace"
		}
		releaseURL := fmt.Sprintf("https://github.com/%s/releases/download/%s", repository, desc)
		for _, arch := range []string{"amd64", "arm64"} {
			k := fmt.Sprintf("STREAMPLACE_URL_LINUX_%s", strings.ToUpper(arch))
			v := fmt.Sprintf("%s/streamplace-%s-linux-%s.tar.gz", releaseURL, desc, arch)
			outMap[k] = v
			macK := fmt.Sprintf("STREAMPLACE_URL_DARWIN_%s", strings.ToUpper(arch))
			macV := fmt.Sprintf("%s/streamplace-%s-darwin-%s.tar.gz", releaseURL, desc, arch)
			outMap[macK] = macV
		}
		for k, v := range outMap {
			out = out + fmt.Sprintf("%s=%s\n", k, v)
		}
	} else if *javascript {
		out = fmt.Sprintf(tmplJS, desc, ts, u)
	} else {
		out = fmt.Sprintf(tmpl, desc, ts, u)
	}

	if *output != "" {
		if err := os.WriteFile(*output, []byte(out), 0644); err != nil {
			return err
		}
	} else {
		fmt.Print(out)
	}
	return nil
}

func branch(ref *plumbing.Reference) string {
	name := os.Getenv("GITHUB_HEAD_REF")
	if name == "" {
		if os.Getenv("GITHUB_REF_TYPE") == "tag" {
			return "latest"
		}
		name = os.Getenv("GITHUB_REF_NAME")
	}
	if name == "" && ref.Name().IsBranch() {
		name = ref.Name().Short()
	}
	if name == "" {
		name = ref.Hash().String()[:8]
	}
	return strings.ReplaceAll(name, "/", "-")
}

// Git struct wrapps Repository class from go-git to add a tag map used to perform queries when describing.
type Git struct {
	TagsMap map[plumbing.Hash]*plumbing.Reference
	*git.Repository
}

// PlainOpen opens a git repository from the given path. It detects if the
// repository is bare or a normal one. If the path doesn't contain a valid
// repository ErrRepositoryNotExists is returned
func PlainOpen(path string) (*Git, error) {
	r, err := git.PlainOpenWithOptions(path, &git.PlainOpenOptions{DetectDotGit: true})
	return &Git{
		make(map[plumbing.Hash]*plumbing.Reference),
		r,
	}, err
}

func (g *Git) getTagMap() error {
	tags, err := g.Tags()
	if err != nil {
		return err
	}

	err = tags.ForEach(func(t *plumbing.Reference) error {
		h, err := g.ResolveRevision(plumbing.Revision(t.Name()))
		if err != nil {
			return err
		}
		g.TagsMap[*h] = t
		return nil
	})

	return err
}

// Describe the reference as 'git describe --tags' will do
func (g *Git) Describe(reference *plumbing.Reference) (string, error) {
	if os.Getenv("STREAMPLACE_VERSION_OVERRIDE") != "" {
		return os.Getenv("STREAMPLACE_VERSION_OVERRIDE"), nil
	}

	if os.Getenv("GITHUB_REF_TYPE") == "tag" {
		name := os.Getenv("GITHUB_REF_NAME")
		hash, err := g.ResolveRevision(plumbing.Revision("refs/tags/" + name))
		if err == nil && *hash == reference.Hash() {
			return name, nil
		}
	}

	// Fetch the reference log
	cIter, err := g.Log(&git.LogOptions{
		// From:  reference.Hash(),
		Order: git.LogOrderCommitterTime,
	})
	if err != nil {
		return "", err
	}

	// Build the tag map
	err = g.getTagMap()
	if err != nil {
		return "", err
	}

	// Search the tag
	var tag *plumbing.Reference
	err = cIter.ForEach(func(c *object.Commit) error {
		t, ok := g.TagsMap[c.Hash]
		if ok {
			tag = t
			return storer.ErrStop
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	head, err := g.Head()
	if err != nil {
		return "", err
	}
	if tag == nil {
		return head.Hash().String()[:8], nil
	}
	return fmt.Sprintf("%s-%s", tag.Name().Short(), head.Hash().String()[:8]), nil
}

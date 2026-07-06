/*
Copyright 2026 The Kube Bind Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Command release computes and creates the next v2 release-candidate tag.
//
// It inspects the tags already present on an upstream remote, finds the highest
// release candidate for the target version (default v2.0.0), and creates the
// next one (e.g. v2.0.0-rc3 if rc2 is the highest upstream). Pushing the tag to
// the remote triggers the Image workflow, so pushing is gated behind -push.
//
// Usage:
//
//	go run ./cmd/release            # compute + create the next rc tag locally
//	go run ./cmd/release -dry-run   # only print what would be created
//	go run ./cmd/release -push      # create and push the tag to the remote
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// options holds the release tool's flags.
type options struct {
	remote  string
	version string
	push    bool
	dryRun  bool
}

func newFlagSet(o *options) *flag.FlagSet {
	fs := flag.NewFlagSet("release", flag.ContinueOnError)
	fs.StringVar(&o.remote, "remote", "origin", "git remote to read existing tags from and push to")
	fs.StringVar(&o.version, "version", "v2.0.0", "target GA version the release candidates lead up to")
	fs.BoolVar(&o.push, "push", false, "push the created tag to the remote (triggers the Image workflow)")
	fs.BoolVar(&o.dryRun, "dry-run", false, "only print the tag that would be created")
	return fs
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := run(ctx)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	var o options
	fs := newFlagSet(&o)
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}

	if !strings.HasPrefix(o.version, "v2.") {
		return fmt.Errorf("target version %q is not a v2 version", o.version)
	}

	tags, err := remoteTags(ctx, o.remote)
	if err != nil {
		return err
	}

	next := nextRC(o.version, tags)
	fmt.Printf("remote %q: highest rc for %s -> next tag %s\n", o.remote, o.version, next)

	if o.dryRun {
		return nil
	}

	if localTagExists(ctx, next) {
		return fmt.Errorf("tag %s already exists locally; delete it first or bump the version", next)
	}

	if err := gitRun(ctx, "tag", "-a", next, "-m", next); err != nil {
		return fmt.Errorf("creating tag %s: %w", next, err)
	}
	fmt.Printf("created annotated tag %s on HEAD\n", next)

	if !o.push {
		fmt.Printf("not pushed. To publish (triggers the Image workflow):\n\n    git push %s %s\n", o.remote, next)
		return nil
	}

	if err := gitRun(ctx, "push", o.remote, next); err != nil {
		return fmt.Errorf("pushing tag %s to %s: %w", next, o.remote, err)
	}
	fmt.Printf("pushed %s to %s — the Image workflow will build ghcr.io/<owner>/konnector:%s\n", next, o.remote, next)
	return nil
}

// rcTagRE matches a release-candidate tag and captures the rc number, e.g.
// "v2.0.0-rc3" -> "3".
var rcTagRE = regexp.MustCompile(`^(v\d+\.\d+\.\d+)-rc(\d+)$`)

// nextRC returns the next release-candidate tag for version, given the set of
// existing tags. If no rc exists for version yet it returns "<version>-rc1".
func nextRC(version string, tags []string) string {
	highest := 0
	for _, t := range tags {
		m := rcTagRE.FindStringSubmatch(t)
		if m == nil || m[1] != version {
			continue
		}
		n, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		if n > highest {
			highest = n
		}
	}
	return fmt.Sprintf("%s-rc%d", version, highest+1)
}

// remoteTags lists the tag names present on the given remote via
// `git ls-remote --tags`, so no local fetch is required. Dereferenced tag refs
// (the "^{}" suffix) are collapsed to their base tag name.
func remoteTags(ctx context.Context, remote string) ([]string, error) {
	out, err := gitOutput(ctx, "ls-remote", "--tags", remote)
	if err != nil {
		return nil, fmt.Errorf("listing tags on remote %q: %w", remote, err)
	}
	seen := map[string]struct{}{}
	var tags []string
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		ref := strings.TrimPrefix(fields[1], "refs/tags/")
		ref = strings.TrimSuffix(ref, "^{}")
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		tags = append(tags, ref)
	}
	sort.Strings(tags)
	return tags, nil
}

// localTagExists reports whether tag already exists in the local repository.
func localTagExists(ctx context.Context, tag string) bool {
	return gitRun(ctx, "rev-parse", "--verify", "--quiet", "refs/tags/"+tag) == nil
}

func gitRun(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func gitOutput(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	return string(out), err
}

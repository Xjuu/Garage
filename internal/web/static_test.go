package web

import (
	"regexp"
	"strings"
	"testing"
)

// versionAssets rewrites URLs inside a page buffer. ReplaceAllFunc hands the
// callback a window into that same buffer, so appending to it writes over the
// bytes that follow — which silently corrupted every tag after the first
// rewrite and took the whole dashboard down. These pin the behaviour.
func TestVersionAssetsDoesNotCorruptThePage(t *testing.T) {
	page := []byte(`<link rel="stylesheet" href="/static/app.css">` +
		`<link rel="icon" href="data:image/svg+xml,<svg/>">` +
		`<script src="/static/app.js"></script>` +
		`<script src="/static/fleet.js"></script>` +
		`<p>Made by Filip Klonowski</p>`)

	got := string(versionAssets(page))

	// Nothing after a rewritten URL may be damaged.
	for _, must := range []string{
		`<link rel="icon" href="data:image/svg+xml,<svg/>">`,
		`</script>`,
		`<p>Made by Filip Klonowski</p>`,
	} {
		if !strings.Contains(got, must) {
			t.Errorf("markup after a rewrite was corrupted; %q is missing from:\n%s", must, got)
		}
	}

	// Each URL is versioned exactly once.
	for _, asset := range []string{"app.css", "app.js", "fleet.js"} {
		re := regexp.MustCompile(regexp.QuoteMeta("/static/"+asset) + `\?v=[a-f0-9]+`)
		if n := len(re.FindAllString(got, -1)); n != 1 {
			t.Errorf("%s was versioned %d times, want 1:\n%s", asset, n, got)
		}
		if strings.Contains(got, asset+"?v=") && strings.Count(got, asset+"?v=") > 1 {
			t.Errorf("%s carries more than one version marker", asset)
		}
	}
	// A doubled marker is the exact symptom of the aliasing bug.
	if strings.Contains(got, "?v=") && regexp.MustCompile(`\?v=[a-f0-9]+\?v=`).MatchString(got) {
		t.Errorf("a version was appended twice:\n%s", got)
	}
}

// An asset that does not exist must be left alone rather than breaking the tag.
func TestVersionAssetsLeavesUnknownFilesIntact(t *testing.T) {
	page := []byte(`<script src="/static/does-not-exist.js"></script>`)
	got := string(versionAssets(page))
	if got != `<script src="/static/does-not-exist.js"></script>` {
		t.Errorf("unknown asset was altered: %s", got)
	}
}

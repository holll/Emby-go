package stabilityrehearsal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// This validates the delivery graph, not a successful GitHub-hosted run or
// artifact provenance. Runtime release acceptance still needs that evidence.
func TestReleaseRequiresSameCommitCI(t *testing.T) {
	type step struct {
		Name            string            `yaml:"name"`
		Uses            string            `yaml:"uses"`
		With            map[string]string `yaml:"with"`
		Run             string            `yaml:"run"`
		If              string            `yaml:"if"`
		ContinueOnError any               `yaml:"continue-on-error"`
	}
	type job struct {
		Uses            string            `yaml:"uses"`
		Needs           string            `yaml:"needs"`
		If              string            `yaml:"if"`
		Permissions     map[string]string `yaml:"permissions"`
		Steps           []step            `yaml:"steps"`
		ContinueOnError any               `yaml:"continue-on-error"`
	}
	type workflow struct {
		On   map[string]any `yaml:"on"`
		Jobs map[string]job `yaml:"jobs"`
	}
	workflows := make(map[string]workflow)
	for _, name := range []string{"ci", "release"} {
		data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", name+".yml"))
		if err != nil {
			t.Fatal(err)
		}
		var w workflow
		if err := yaml.Unmarshal(data, &w); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		workflows[name] = w
	}
	ci, release := workflows["ci"], workflows["release"]
	if _, ok := ci.On["workflow_call"]; !ok {
		t.Fatal("CI is not callable from release")
	}
	gate := release.Jobs["verify"]
	if gate.Uses != "./.github/workflows/ci.yml" || gate.If != "" || gate.Permissions["contents"] != "read" {
		t.Fatalf("release must call local CI unconditionally with read-only contents: %+v", gate)
	}
	build := release.Jobs["build"]
	if build.Needs != "verify" || build.If != "" {
		t.Fatalf("release must depend on successful CI without always()/override: %+v", build)
	}
	for _, name := range []string{"go", "race", "vuln", "web"} {
		j, ok := ci.Jobs[name]
		if !ok || j.If != "" {
			t.Fatalf("CI must keep unconditional %s gate", name)
		}
	}
	for workflowName, w := range workflows {
		for jobName, j := range w.Jobs {
			if j.ContinueOnError != nil {
				t.Errorf("%s/%s must not override failure propagation", workflowName, jobName)
			}
			if j.Uses != "" {
				continue
			}
			checkout := false
			for _, s := range j.Steps {
				if s.ContinueOnError != nil {
					t.Errorf("%s/%s/%s must not ignore step failure", workflowName, jobName, s.Name)
				}
				if strings.HasPrefix(s.Uses, "actions/checkout@") {
					checkout = true
					if s.With["ref"] != "${{ github.sha }}" || s.If != "" {
						t.Errorf("%s/%s checkout must use event SHA unconditionally", workflowName, jobName)
					}
				}
			}
			if !checkout {
				t.Errorf("%s/%s has no checkout pinned to event SHA", workflowName, jobName)
			}
		}
	}
	// Version execution and checksums must precede the only publishing step.
	var versionChecked, checksummed, uploaded bool
	for _, s := range build.Steps {
		if strings.Contains(s.Run, "commit=\"$(git rev-parse HEAD)\"") &&
			strings.Contains(s.Run, "test \"${actual}\" = \"${expected}\"") {
			versionChecked = true
		}
		if strings.Contains(s.Run, "sha256sum -c SHA256SUMS") {
			checksummed = true
		}
		if strings.HasPrefix(s.Uses, "softprops/action-gh-release@") {
			if !versionChecked || !checksummed || s.If != "" || s.With["files"] != "dist/*" {
				t.Fatal("publish must follow same-build version/checksum verification without bypass")
			}
			uploaded = true
		}
	}
	if !uploaded {
		t.Fatal("release upload step missing")
	}
}

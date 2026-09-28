package textnorm

import (
	"context"
	"regexp"
	"testing"
)

func TestManifestCarriesVersion(t *testing.T) {
	engine, err := Compile(Config{Rules: []CandidateRule{
		RegexRule{ID: "x", Priority: 10, Match: regexp.MustCompile(`X`), Replacement: "y"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "X"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Version != Version {
		t.Fatalf("manifest version = %q, want %q", res.Manifest.Version, Version)
	}
	if res.Manifest.Version != "v0.1.0" {
		t.Fatalf("pin check failed: %q", res.Manifest.Version)
	}
}

func TestVersionSemverShape(t *testing.T) {
	if !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(Version) {
		t.Fatalf("Version %q is not semver-shaped", Version)
	}
}

func TestModulePath(t *testing.T) {
	if ModulePath != "github.com/F31/textnorm" {
		t.Fatalf("ModulePath = %q", ModulePath)
	}
}

func TestProfilesContract(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Profiles() {
		if p.Name == "" || p.Version <= 0 {
			t.Fatalf("profile info invalid: %+v", p)
		}
		if seen[p.Name] {
			t.Fatalf("duplicate profile %q", p.Name)
		}
		seen[p.Name] = true
		if v, err := ProfileVersion(p.Name); err != nil || v != p.Version {
			t.Fatalf("ProfileVersion(%q) = %d, %v; want %d", p.Name, v, err, p.Version)
		}
		if _, err := ProfileRules(p.Name); err != nil {
			t.Fatalf("ProfileRules(%q): %v", p.Name, err)
		}
	}
	if _, err := ProfileVersion("no-such-profile"); err == nil {
		t.Fatal("ProfileVersion should reject unknown name")
	}
}

func TestCompileProfileStampsManifest(t *testing.T) {
	engine, err := CompileProfile(ProfileConservative)
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Normalize(context.Background(), Request{Text: "PM2.5"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Manifest.Profile != ProfileConservative {
		t.Fatalf("manifest profile = %q, want %q", res.Manifest.Profile, ProfileConservative)
	}
	if res.Manifest.ProfileVersion != profileVersions[ProfileConservative] {
		t.Fatalf("manifest profile version = %d, want %d", res.Manifest.ProfileVersion, profileVersions[ProfileConservative])
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(res.Manifest.ConfigHash) {
		t.Fatalf("manifest config hash = %q, want sha256 hex", res.Manifest.ConfigHash)
	}

	again, err := CompileProfile(ProfileConservative)
	if err != nil {
		t.Fatal(err)
	}
	againRes, err := again.Normalize(context.Background(), Request{Text: "PM2.5"})
	if err != nil {
		t.Fatal(err)
	}
	if againRes.Manifest.ConfigHash != res.Manifest.ConfigHash {
		t.Fatalf("config hash changed across identical profile compiles: %q != %q", againRes.Manifest.ConfigHash, res.Manifest.ConfigHash)
	}
}

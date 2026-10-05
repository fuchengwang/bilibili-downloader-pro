package update

import "testing"

func TestStableVersionsAndPlatforms(t *testing.T) {
	for _, value := range []string{"0.0.0", "1.9.0", "v1.10.0", " 1.2.3 "} {
		if _, _, err := ParseVersion(value); err != nil { t.Fatalf("%q: %v", value, err) }
	}
	for _, value := range []string{"", "1.2", "1.2.3.4", "01.2.3", "1.2.03", "1.2.3-beta", "1.2.3+build", "+1.2.3", "4294967296.0.0", "1. 2.3"} {
		if _, _, err := ParseVersion(value); err == nil { t.Fatalf("invalid version accepted: %q", value) }
	}
	_, a, _ := ParseVersion("1.9.0")
	_, b, _ := ParseVersion("v1.10.0")
	if a[1] >= b[1] { t.Fatal("version comparison is not numeric") }
	if !ValidPlatform("darwin", "universal", true) || ValidPlatform("darwin", "universal", false) || ValidPlatform("windows", "universal", true) || ValidPlatform("android", "arm64", true) { t.Fatal("platform validation failed") }
}

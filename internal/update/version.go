package update

import (
	"strconv"
	"strings"
)

// version is a parsed semantic version: v1.2.3 or 1.2.3-rc.1.
type version struct {
	major, minor, patch int
	pre                 string // "" for a release
}

func parseVersion(s string) (version, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	core, pre, _ := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return version{}, false
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 {
			return version{}, false
		}
		n[i] = v
	}
	return version{n[0], n[1], n[2], pre}, true
}

// less reports a < b; a pre-release sorts before its release.
func (a version) less(b version) bool {
	if a.major != b.major {
		return a.major < b.major
	}
	if a.minor != b.minor {
		return a.minor < b.minor
	}
	if a.patch != b.patch {
		return a.patch < b.patch
	}
	switch {
	case a.pre == b.pre:
		return false
	case a.pre == "":
		return false
	case b.pre == "":
		return true
	}
	return lessPre(a.pre, b.pre)
}

// lessPre compares pre-release identifiers as semver does: numeric ones
// numerically, others as text, and fewer identifiers first.
func lessPre(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if as[i] == bs[i] {
			continue
		}
		an, aerr := strconv.Atoi(as[i])
		bn, berr := strconv.Atoi(bs[i])
		switch {
		case aerr == nil && berr == nil:
			return an < bn
		case aerr == nil:
			return true
		case berr == nil:
			return false
		}
		return as[i] < bs[i]
	}
	return len(as) < len(bs)
}

// Newer reports whether latest is a newer version than current. A current
// version that is not a version (a development build from a commit) is
// never offered an update.
func Newer(current, latest string) bool {
	c, ok := parseVersion(current)
	if !ok {
		return false
	}
	l, ok := parseVersion(latest)
	if !ok {
		return false
	}
	return c.less(l)
}

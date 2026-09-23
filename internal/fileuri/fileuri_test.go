package fileuri

import (
	"net/url"
	"testing"
)

func TestDrivePathsHaveNoURIAuthority(t *testing.T) {
	for _, test := range []struct{ path, want string }{{`C:\Users\fixture\file name.db`, "file:///C:/Users/fixture/file%20name.db"}, {"C:/data/a%20.db", "file:///C:/data/a%2520.db"}, {"/tmp/a b.db", "file:///tmp/a%20b.db"}} {
		u := url.URL{Scheme: "file", Path: Path(test.path)}
		if u.String() != test.want {
			t.Errorf("%q => %q, want %q", test.path, u.String(), test.want)
		}
		parsed, err := url.Parse(u.String())
		if err != nil || parsed.Host != "" {
			t.Errorf("local path became a remote authority: %+v %v", parsed, err)
		}
	}
}

package autonomy

import "testing"

func TestDomainOf(t *testing.T) {
	cases := []struct {
		cap, res, want string
	}{
		{"cap:http.post", "https://x/y", "http"},
		{"fs:write:/reports/*", "", "fs"},
		{"mail:send", "", "mail"},
		{"db", "", "db"},
		{"", "url:https://x", "url"},
		{"", "file", "file"},
		{"", "", DomainUnknown},
		{"cap:", "", DomainUnknown}, // prefixo cap: sem segmento e sem recurso
	}
	for _, c := range cases {
		if got := DomainOf(c.cap, c.res); got != c.want {
			t.Errorf("DomainOf(%q,%q) = %q; quer %q", c.cap, c.res, got, c.want)
		}
	}
}

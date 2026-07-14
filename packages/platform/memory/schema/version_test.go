package schema_test

import (
	"errors"
	"testing"

	"github.com/aos-ref/platform/memory/schema"
)

func TestParseVersion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		wantErr bool
		want    schema.Version
	}{
		{in: "1.0.0", want: schema.Version{Major: 1}},
		{in: "2.3.4", want: schema.Version{Major: 2, Minor: 3, Patch: 4}},
		{in: " 1.2.3 ", want: schema.Version{Major: 1, Minor: 2, Patch: 3}},
		{in: "0.0.0", want: schema.Version{}},
		{in: "1.0", wantErr: true},
		{in: "1.0.0.0", wantErr: true},
		{in: "v1.0.0", wantErr: true},
		{in: "1..0", wantErr: true},
		{in: "1.0.-1", wantErr: true},
		{in: "1.0.x", wantErr: true},
		{in: "", wantErr: true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.in, func(t *testing.T) {
			t.Parallel()
			got, err := schema.ParseVersion(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("ParseVersion(%q): esperava erro", c.in)
				}
				if !errors.Is(err, schema.ErrInvalidVersion) {
					t.Fatalf("ParseVersion(%q): erro = %v, quero ErrInvalidVersion", c.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseVersion(%q): erro inesperado %v", c.in, err)
			}
			if got != c.want {
				t.Fatalf("ParseVersion(%q) = %+v, quero %+v", c.in, got, c.want)
			}
			if got.String() != normalize(c.in) {
				t.Fatalf("String() = %q, quero %q", got.String(), normalize(c.in))
			}
		})
	}
}

func normalize(s string) string {
	v, _ := schema.ParseVersion(s)
	return v.String()
}

func TestCompareAndOrder(t *testing.T) {
	t.Parallel()
	mk := func(s string) schema.Version { v, _ := schema.ParseVersion(s); return v }
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "2.0.0", -1},
		{"2.0.0", "1.9.9", 1},
		{"1.2.0", "1.3.0", -1},
		{"1.2.3", "1.2.4", -1},
		{"1.2.4", "1.2.3", 1},
	}
	for _, c := range cases {
		if got := mk(c.a).Compare(mk(c.b)); got != c.want {
			t.Fatalf("Compare(%s,%s) = %d, quero %d", c.a, c.b, got, c.want)
		}
		if c.want < 0 && !mk(c.a).Less(mk(c.b)) {
			t.Fatalf("Less(%s,%s) devia ser true", c.a, c.b)
		}
		if c.want == 0 && !mk(c.a).Equal(mk(c.b)) {
			t.Fatalf("Equal(%s,%s) devia ser true", c.a, c.b)
		}
	}
}

func TestClassify(t *testing.T) {
	t.Parallel()
	mk := func(s string) schema.Version { v, _ := schema.ParseVersion(s); return v }
	cases := []struct {
		from, to string
		want     schema.ChangeKind
	}{
		{"1.0.0", "1.0.0", schema.ChangeNone},
		{"1.0.0", "1.0.1", schema.ChangePatch},
		{"1.0.0", "1.1.0", schema.ChangeMinor},
		{"1.0.0", "2.0.0", schema.ChangeMajor},
		{"1.5.0", "2.0.0", schema.ChangeMajor},
		{"2.0.0", "1.0.0", schema.ChangeMajor}, // downgrade de MAJOR ainda é MAJOR
		{"1.2.3", "1.2.9", schema.ChangePatch},
	}
	for _, c := range cases {
		if got := schema.Classify(mk(c.from), mk(c.to)); got != c.want {
			t.Fatalf("Classify(%s,%s) = %s, quero %s", c.from, c.to, got, c.want)
		}
	}
}

package filter

import "testing"

func TestEmptyAllowsEverything(t *testing.T) {
	r, err := Parse(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Empty() {
		t.Fatal("expected empty rules")
	}
	if !r.Match("anything.example", 443) || !r.MatchHost("1.2.3.4") {
		t.Fatal("empty rules must allow all")
	}
	var nilRules *Rules
	if !nilRules.Match("x", 1) {
		t.Fatal("nil rules must allow all")
	}
}

func TestDenyWins(t *testing.T) {
	r, err := Parse([]string{"10.0.0.0/8", "*.example.com"}, []string{"10.1.2.3", "*.secret.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !r.MatchHost("10.5.0.1") {
		t.Fatal("10.5.0.1 should be allowed by cidr")
	}
	if r.MatchHost("10.1.2.3") {
		t.Fatal("deny must win over allow cidr")
	}
	if !r.MatchHost("api.example.com") {
		t.Fatal("wildcard allow should match")
	}
	if r.MatchHost("db.secret.example.com") {
		t.Fatal("deny wildcard must win")
	}
	if r.MatchHost("other.org") {
		t.Fatal("non-listed host must be denied when allow is non-empty")
	}
}

func TestPortPatterns(t *testing.T) {
	r, err := Parse(nil, []string{"*:25", "example.com:443"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Match("example.com", 25) {
		t.Fatal("port 25 should be denied for any host")
	}
	if r.Match("example.com", 443) {
		t.Fatal("example.com:443 should be denied")
	}
	if !r.Match("example.com", 443+1) {
		t.Fatal("other ports on example.com should pass")
	}
	if !r.Match("other.com", 443) {
		t.Fatal("port match is host-specific")
	}
}

func TestHostOnlyIgnoresPort(t *testing.T) {
	r, err := Parse([]string{"192.168.1.0/24"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// MatchHost ignores the port component: source ports are ephemeral.
	if !r.MatchHost("192.168.1.20") {
		t.Fatal("source match should ignore port")
	}
	if r.MatchHost("192.168.2.20") {
		t.Fatal("outside cidr must not match")
	}
}

func TestBadPatterns(t *testing.T) {
	cases := [][]string{
		{"[unclosed"},
		{"10.0.0.0/999"},
		{"host:99999"},
		{"host:notaport"},
	}
	for _, c := range cases {
		if err := ValidateLists(c, nil); err == nil {
			t.Fatalf("pattern %q should be invalid", c)
		}
	}
}

func TestTooManyRules(t *testing.T) {
	list := make([]string, maxRules+1)
	for i := range list {
		list[i] = "h.example"
	}
	if err := ValidateLists(list, nil); err == nil {
		t.Fatal("expected too-many-rules error")
	}
}

func TestCIDRWithPort(t *testing.T) {
	r, err := Parse(nil, []string{"10.0.0.0/8:5432"})
	if err != nil {
		t.Fatalf("cidr with port should parse: %v", err)
	}
	if r.Match("10.1.1.1", 5432) {
		t.Fatal("deny cidr:port should match")
	}
	if !r.Match("10.1.1.1", 80) {
		t.Fatal("cidr:port should not match other ports")
	}
}

func TestValidateSourceRejectsPorts(t *testing.T) {
	if err := ValidateSource([]string{"10.0.0.0/8", "*.example.com", "::1", "203.0.113.7"}, []string{"192.0.2.0/24"}); err != nil {
		t.Fatalf("plain source filters: %v", err)
	}
	for _, bad := range [][2][]string{
		{{"*:25"}, nil},
		{nil, {"192.0.2.5:443"}},
		{nil, {"10.0.0.0/8:22"}},
		{{"bad[x"}, nil},
	} {
		if err := ValidateSource(bad[0], bad[1]); err == nil {
			t.Errorf("ValidateSource(%v, %v) accepted", bad[0], bad[1])
		}
	}
	// destination filters keep ports
	if err := ValidateLists(nil, []string{"*:25"}); err != nil {
		t.Fatalf("destination filter with port: %v", err)
	}
}

package scanner

import (
	"testing"
)

func TestAuto_UnknownScannerRejected(t *testing.T) {
	_, err := Auto(&Config{Scanner: "weird", PortMin: 40000, PortMax: 40500})
	if err == nil {
		t.Fatalf("Auto(weird): expected error")
	}
}

func TestAuto_LsofSelectable(t *testing.T) {
	sc, err := Auto(&Config{Scanner: "lsof", PortMin: 40000, PortMax: 40500})
	if err != nil {
		t.Fatalf("Auto(lsof): %v", err)
	}
	if sc.Name() != "lsof" {
		t.Fatalf("Name() = %q, want lsof", sc.Name())
	}
}

func TestAuto_NilConfigRejected(t *testing.T) {
	if _, err := Auto(nil); err == nil {
		t.Fatalf("Auto(nil): expected error")
	}
}

func TestParseLsofF_IgnoresUDPByProtocol(t *testing.T) {
	// lsof with -iTCP already filters to TCP, but the parser must
	// still drop any stray non-TCP P records so the guarantee holds
	// even if the caller tweaks argv.
	out := []byte("p123\nPUDP\nn*:40100\np124\nPTCP\nn*:40101\n")
	got := parseLsofF(out, 40000, 40500, 0)
	if len(got) != 1 {
		t.Fatalf("want 1 listener, got %d: %+v", len(got), got)
	}
	if got[0].Port != 40101 || got[0].PID != 124 || got[0].Protocol != "tcp" {
		t.Fatalf("unexpected listener: %+v", got[0])
	}
}

func TestParseLsofF_ExcludesOwnPID(t *testing.T) {
	out := []byte("p777\nPTCP\nn*:40100\np888\nPTCP\nn127.0.0.1:40200\n")
	got := parseLsofF(out, 40000, 40500, 777)
	if len(got) != 1 {
		t.Fatalf("want 1 listener after excluding self, got %d: %+v", len(got), got)
	}
	if got[0].PID != 888 {
		t.Fatalf("wrong pid survived: %+v", got[0])
	}
}

func TestParseLsofF_PortRangeFilter(t *testing.T) {
	out := []byte("p100\nPTCP\nn*:22\np101\nPTCP\nn*:40123\np102\nPTCP\nn*:60000\n")
	got := parseLsofF(out, 40000, 40500, 0)
	if len(got) != 1 || got[0].Port != 40123 {
		t.Fatalf("expected only 40123, got %+v", got)
	}
}

func TestParseLsofF_AggregatesDualStack(t *testing.T) {
	out := []byte("p500\nPTCP\nn*:40100\nn[::1]:40100\n")
	got := parseLsofF(out, 40000, 40500, 0)
	if len(got) != 1 {
		t.Fatalf("want 1 aggregated listener, got %d: %+v", len(got), got)
	}
	if len(got[0].Addrs) != 2 {
		t.Fatalf("want 2 addrs, got %v", got[0].Addrs)
	}
}

func TestSplitHostPort(t *testing.T) {
	cases := []struct {
		in   string
		host string
		port int
		ok   bool
	}{
		{"*:40123", "0.0.0.0", 40123, true},
		{"127.0.0.1:40123", "127.0.0.1", 40123, true},
		{"[::1]:40123", "::1", 40123, true},
		{"[::]:40123", "::", 40123, true},
		{"malformed", "", 0, false},
		{"*:notaport", "", 0, false},
	}
	for _, c := range cases {
		h, p, ok := splitHostPort(c.in)
		if ok != c.ok || h != c.host || p != c.port {
			t.Errorf("splitHostPort(%q) = (%q, %d, %v), want (%q, %d, %v)",
				c.in, h, p, ok, c.host, c.port, c.ok)
		}
	}
}

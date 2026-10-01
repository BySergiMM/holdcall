package console

import (
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/BySergiMM/holdcall/engine/internal/journal"
)

// apiPaths is every route the console serves besides the page.
var apiPaths = []string{
	"/api/snapshot", "/api/events?since=0", "/api/sessions/s1",
	"/api/policy", "/api/explain?tool=rm", "/api/pending",
}

// A distinctive name in the journal, so that a response that leaks the record
// can be told from one that merely has a 200 in it.
const leakMarker = "connector-nobody-may-learn-the-name-of"

// untokened serves a console whose requests carry exactly what the test sends,
// unlike start, which adds the token.
func untokened(t *testing.T) (*httptest.Server, *Server) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "holdcall.db")
	w, err := journal.Open(path, "test-machine")
	if err != nil {
		t.Fatal(err)
	}
	w.Append(session("s1", leakMarker))
	w.Append(call("s1", 1, "create_issue"))
	w.Close()
	reader, err := journal.OpenReadOnly(path, "test-machine")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })

	s := New(reader, filepath.Join(t.TempDir(), "absent.sock"))
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, s
}

func fetch(t *testing.T, srv *httptest.Server, path string, header http.Header) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return res.StatusCode, res.Header, string(body)
}

// Any process on this machine can open a connection to a loopback port, and
// /api/pending carries the real arguments of calls an agent is making right
// now. What stands between that process and the record is the token.
func TestNothingButThePageIsReadWithoutTheToken(t *testing.T) {
	srv, _ := untokened(t)

	// The routes that exist, and some that do not: a path added later is shut
	// until someone decides otherwise, so an unknown one is refused, not 404.
	paths := append([]string{"/api/nothing-here", "/index.html", "/static/x", "/console.go", "//api/snapshot", "/api"}, apiPaths...)
	for _, p := range paths {
		status, hdr, body := fetch(t, srv, p, nil)
		if status != http.StatusUnauthorized {
			t.Errorf("%s with no token returned %d, want 401", p, status)
		}
		if hdr.Get("WWW-Authenticate") == "" {
			t.Errorf("%s: a 401 should say what it wants", p)
		}
		if strings.Contains(body, leakMarker) || strings.Contains(body, "create_issue") {
			t.Errorf("%s answered a request with no token with the record: %s", p, body)
		}
	}
}

// The token travels in one header and nowhere else. A query string lands in
// history, logs and Referer; a cookie is sent to every server on 127.0.0.1.
func TestTheTokenIsAcceptedInTheAuthorizationHeaderOnly(t *testing.T) {
	srv, s := untokened(t)
	tok := s.Token()
	other := strings.Repeat("0", len(tok))
	if other == tok {
		other = strings.Repeat("1", len(tok))
	}
	flip := byte('a')
	if tok[len(tok)-1] == 'a' {
		flip = 'b'
	}
	flipped := tok[:len(tok)-1] + string(flip)

	refused := map[string]http.Header{
		"another token of the same length": {"Authorization": {"Bearer " + other}},
		"the last character changed":       {"Authorization": {"Bearer " + flipped}},
		"a prefix of the token":            {"Authorization": {"Bearer " + tok[:len(tok)/2]}},
		"the token and one more character": {"Authorization": {"Bearer " + tok + "0"}},
		"an empty bearer":                  {"Authorization": {"Bearer "}},
		"no scheme":                        {"Authorization": {tok}},
		"another scheme":                   {"Authorization": {"Basic " + tok}},
		"a cookie":                         {"Cookie": {"token=" + tok}},
		"a custom header":                  {"X-Holdcall-Token": {tok}},
	}
	for name, hdr := range refused {
		for _, p := range apiPaths {
			if status, _, body := fetch(t, srv, p, hdr); status != http.StatusUnauthorized {
				t.Errorf("%s, reading %s: %d, want 401 (%s)", name, p, status, body)
			}
		}
	}

	for _, p := range apiPaths {
		if status, _, body := fetch(t, srv, p+queryJoiner(p)+"token="+tok, nil); status != http.StatusUnauthorized {
			t.Errorf("the token in the query string, reading %s: %d, want 401 (%s)", p, status, body)
		}
	}
}

func queryJoiner(p string) string {
	if strings.Contains(p, "?") {
		return "&"
	}
	return "?"
}

func TestTheTokenOpensEveryRouteTheConsoleServes(t *testing.T) {
	srv, s := untokened(t)
	hdr := http.Header{"Authorization": {"Bearer " + s.Token()}}
	for _, p := range apiPaths {
		if status, _, body := fetch(t, srv, p, hdr); status != http.StatusOK {
			t.Errorf("%s with the token returned %d: %s", p, status, body)
		}
	}
	if _, _, body := fetch(t, srv, "/api/snapshot", hdr); !strings.Contains(body, `"entries"`) {
		t.Errorf("the token should open the record, got %s", body)
	}
}

// The token does not stand in for the rest. A page on another site that has
// rebound its own name to this address does not have it anyway; but the rule
// that a name which is not loopback is refused has to hold for whoever does.
func TestTheTokenDoesNotOpenARebornName(t *testing.T) {
	srv, s := untokened(t)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/snapshot", nil)
	req.Host = "attacker.example:7717"
	req.Header.Set("Authorization", "Bearer "+s.Token())
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusMisdirectedRequest {
		t.Errorf("a rebound name with the token got %d, want 421", res.StatusCode)
	}
}

// The page has to load before it can be given a token, so it is the one thing
// served without. It is one file in the binary and holds nothing of any journal.
func TestThePageLoadsWithoutTheTokenAndHoldsNothingFromTheJournal(t *testing.T) {
	srv, _ := untokened(t)
	status, _, body := fetch(t, srv, "/", nil)
	if status != http.StatusOK {
		t.Fatalf("the page returned %d with no token; it has to load to be given one", status)
	}
	if !strings.Contains(body, "holdcall console") {
		t.Error("the console page was not served")
	}
	if strings.Contains(body, leakMarker) || strings.Contains(body, "create_issue") {
		t.Error("the page carries something from the journal")
	}
}

// 128 bits is the least anyone would call unguessable over a socket; this is
// 256, from the operating system, new for every Server.
func TestEveryConsoleHasATokenOfItsOwnThatCannotBeGuessed(t *testing.T) {
	if tokenBytes < 16 {
		t.Fatalf("tokenBytes = %d: under 128 bits", tokenBytes)
	}
	seen := map[string]bool{}
	for range 64 {
		tok := New(nil, "").Token()
		raw, err := hex.DecodeString(tok)
		if err != nil || len(raw) != tokenBytes {
			t.Fatalf("token %q is not %d bytes of hex", tok, tokenBytes)
		}
		if seen[tok] {
			t.Fatalf("two consoles were given the same token %q", tok)
		}
		seen[tok] = true
		if strings.Trim(tok, "0") == "" {
			t.Fatal("a token of nothing but zeros")
		}
	}
}

// A browser cannot be made to send what the page does not attach, and a page
// that asks for something without the token is a page that stops working the
// day the console needs it. Every request it makes goes through api().
func TestEveryRequestThePageMakesGoesThroughTheOneFunctionThatCarriesTheToken(t *testing.T) {
	raw, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(raw)

	if n := strings.Count(page, "fetch("); n != 1 {
		t.Errorf("the page calls fetch( %d times; exactly one, inside api(), may", n)
	}
	if !strings.Contains(page, `Authorization: "Bearer " + token`) {
		t.Error("the page does not send the token as a bearer credential")
	}
	calls := regexp.MustCompile(`(\w+)\("(/api/[a-z]+)`).FindAllStringSubmatch(page, -1)
	if len(calls) == 0 {
		t.Fatal("found no request to /api in the page")
	}
	for _, c := range calls {
		if c[1] != "api" {
			t.Errorf("%s(%q...) asks for the record without going through api()", c[1], c[2])
		}
	}
	// The token must not be put anywhere a server or another page could read it.
	for _, bad := range []string{"document.cookie", "?token=", "&token=", "localStorage.setItem(\"holdcall.console.token"} {
		if strings.Contains(page, bad) {
			t.Errorf("the page puts the token somewhere it should not: %s", bad)
		}
	}
}

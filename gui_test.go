package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// startGUI runs the GUI server on a free loopback port.
func startGUI(t *testing.T, withEngine bool) (*guiServer, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s, err := newGUIServer(ctx, ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	s.jobs = 2
	srv := &http.Server{Handler: s.handler()}
	go srv.Serve(ln)
	go s.runQueue()
	if withEngine {
		go s.warmEngine()
	}
	t.Cleanup(func() {
		cancel()
		srv.Close()
		if s.eng != nil {
			s.eng.Close()
		}
	})
	return s, s.origin
}

func req(t *testing.T, method, url string, body io.Reader, hdr map[string]string) *http.Response {
	t.Helper()
	r, _ := http.NewRequest(method, url, body)
	for k, v := range hdr {
		if k == "Host" {
			r.Host = v
		} else {
			r.Header.Set(k, v)
		}
	}
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func TestGUISecurity(t *testing.T) {
	s, base := startGUI(t, false)
	tok := map[string]string{"X-Token": s.token}

	cases := []struct {
		name   string
		method string
		path   string
		hdr    map[string]string
		want   int
	}{
		{"page is public", "GET", "/", nil, 200},
		{"assets are public", "GET", "/assets/app.css", nil, 200},
		{"API needs the token", "GET", "/api/licenses", nil, 401},
		{"wrong token", "GET", "/api/licenses", map[string]string{"X-Token": "nope"}, 401},
		{"token in header", "GET", "/api/licenses", tok, 200},
		{"token in query for reads", "GET", "/api/licenses?t=" + s.token, nil, 200},
		{"writes need the header, not the query", "POST", "/api/quit?t=" + s.token, nil, 401},
		{"foreign Host (DNS rebinding)", "GET", "/", map[string]string{"Host": "evil.example:80"}, 403},
		{"foreign Origin on a write", "POST", "/api/quit", map[string]string{"X-Token": s.token, "Origin": "http://evil.example"}, 403},
	}
	for _, c := range cases {
		if got := req(t, c.method, base+c.path, nil, c.hdr).StatusCode; got != c.want {
			t.Errorf("%s: status %d, want %d", c.name, got, c.want)
		}
	}
	res := req(t, "GET", base+"/", nil, nil)
	if csp := res.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("missing Content-Security-Policy, got %q", csp)
	}
	if res.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Error("the token in the URL must not leak through Referer")
	}
}

type sseEvent struct{ name, data string }

// events subscribes to the server's event stream.
func events(t *testing.T, s *guiServer, base string) <-chan sseEvent {
	t.Helper()
	res := req(t, "GET", base+"/api/events?t="+s.token, nil, nil)
	ch := make(chan sseEvent, 256)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var ev sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				ev.name = line[7:]
			case strings.HasPrefix(line, "data: "):
				ev.data = line[6:]
			case line == "" && ev.name != "":
				ch <- ev
				ev = sseEvent{}
			}
		}
	}()
	return ch
}

// waitJobs reads events until every listed file reaches state.
func waitJobs(t *testing.T, ch <-chan sseEvent, state jobState, ids ...string) map[string]jobView {
	t.Helper()
	got := map[string]jobView{}
	deadline := time.After(90 * time.Second)
	for {
		done := 0
		for _, id := range ids {
			if got[id].State == state {
				done++
			}
		}
		if done == len(ids) {
			return got
		}
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("event stream closed")
			}
			if ev.name == "job" {
				var v jobView
				json.Unmarshal([]byte(ev.data), &v)
				if v.State == stateFailed && state != stateFailed {
					t.Fatalf("%s failed: %s", v.File, v.Error)
				}
				got[v.ID] = v
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s: %+v", state, got)
		}
	}
}

func upload(t *testing.T, s *guiServer, base string, files map[string][]byte) []jobView {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for name, data := range files {
		w, _ := mw.CreateFormFile("file", name)
		w.Write(data)
	}
	mw.Close()
	res := req(t, "POST", base+"/api/files", &body, map[string]string{"X-Token": s.token, "Content-Type": mw.FormDataContentType()})
	if res.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("upload: %d %s", res.StatusCode, b)
	}
	var views []jobView
	json.NewDecoder(res.Body).Decode(&views)
	return views
}

func TestGUIConvertFlow(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the PDF engine; skipped with -short")
	}
	s, base := startGUI(t, true)
	ch := events(t, s, base)
	if ev := <-ch; ev.name != "snapshot" {
		t.Fatalf("first event is %q, want snapshot", ev.name)
	}

	deck := makePDF([]testPage{{W: 960, H: 540}, {W: 960, H: 540}})
	scan := makePDF([]testPage{{W: 612, H: 792, Scan: &Size{1275, 1650}}, {W: 612, H: 792, Scan: &Size{1275, 1650}}})
	views := upload(t, s, base, map[string][]byte{"Slide deck.pdf": deck, "كتاب.pdf": scan})
	if len(views) != 2 {
		t.Fatalf("got %d jobs, want 2", len(views))
	}
	ids := []string{views[0].ID, views[1].ID}
	ready := waitJobs(t, ch, stateReady, ids...)

	byFile := map[string]jobView{}
	for _, v := range ready {
		byFile[v.File] = v
	}
	if v := byFile["Slide deck.pdf"]; v.Pages != 2 || v.Orient != "landscape" || v.ScanPPI != 0 || !v.HasCover {
		t.Errorf("deck inspected as %+v", v)
	}
	if v := byFile["كتاب.pdf"]; v.ScanPPI != 150 || v.Orient != "portrait" {
		t.Errorf("scan inspected as %+v, want a 150 ppi portrait scan", v)
	}

	// Invalid settings are refused before anything is queued.
	bad := defaultSettings()
	bad.Lang = "not a language"
	b, _ := json.Marshal(convertRequest{IDs: ids, Settings: bad})
	if res := req(t, "POST", base+"/api/convert", bytes.NewReader(b), map[string]string{"X-Token": s.token}); res.StatusCode != 400 {
		t.Errorf("bad settings: status %d, want 400", res.StatusCode)
	}

	set := defaultSettings()
	set.Direction, set.Lang = "rtl", "ar"
	b, _ = json.Marshal(convertRequest{IDs: ids, Settings: set, Titles: map[string]string{byFile["Slide deck.pdf"].ID: "My Deck"}})
	if res := req(t, "POST", base+"/api/convert", bytes.NewReader(b), map[string]string{"X-Token": s.token}); res.StatusCode != 202 {
		t.Fatalf("convert: status %d", res.StatusCode)
	}
	done := waitJobs(t, ch, stateDone, ids...)

	for id, v := range done {
		r := v.Result
		if r == nil || !r.Validated || !r.Passed {
			t.Fatalf("%s: result %+v", v.File, r)
		}
		res := req(t, "GET", base+"/api/jobs/"+id+"/epub?t="+s.token, nil, nil)
		data, _ := io.ReadAll(res.Body)
		if probs := validatePackage(data, Expect{Pages: 2, Grayscale: true}); len(probs) != 0 {
			t.Errorf("%s: downloaded EPUB fails validation: %v", v.File, probs)
		}
		if cd := res.Header.Get("Content-Disposition"); !strings.Contains(cd, "filename*=UTF-8''") {
			t.Errorf("%s: Content-Disposition %q lacks a UTF-8 filename", v.File, cd)
		}
		opf := string(zipFile(t, data, "OEBPS/content.opf"))
		if !strings.Contains(opf, `page-progression-direction="rtl"`) || !strings.Contains(opf, "<dc:language>ar</dc:language>") {
			t.Errorf("%s: settings were not applied", v.File)
		}
		if v.File == "Slide deck.pdf" && !strings.Contains(opf, "<dc:title>My Deck</dc:title>") {
			t.Error("the edited title was not used")
		}
		if v.File == "كتاب.pdf" && (r.DPI != 150 || !r.Scan) {
			t.Errorf("scan converted at %d DPI (scan=%v), want its native 150", r.DPI, r.Scan)
		}
		cov := req(t, "GET", base+"/api/jobs/"+id+"/cover?t="+s.token, nil, nil)
		if _, err := jpeg.Decode(cov.Body); err != nil {
			t.Errorf("%s: cover is not a JPEG: %v", v.File, err)
		}

		// The preview reads the book back, and serves its pages.
		var pb previewBook
		pr := req(t, "GET", base+"/api/jobs/"+id+"/preview?t="+s.token, nil, nil)
		if err := json.NewDecoder(pr.Body).Decode(&pb); err != nil || len(pb.Pages) != 2 || pb.Direction != "rtl" {
			t.Errorf("%s: preview %+v, %v", v.File, pb, err)
		}
		pg := req(t, "GET", base+"/api/jobs/"+id+"/pages/1?t="+s.token, nil, nil)
		if m, err := jpeg.Decode(pg.Body); err != nil || m.Bounds().Dx() != pb.Pages[1].W {
			t.Errorf("%s: page 2 of the preview: %v", v.File, err)
		}
		for _, n := range []string{"2", "-1", "x"} {
			if res := req(t, "GET", base+"/api/jobs/"+id+"/pages/"+n+"?t="+s.token, nil, nil); res.StatusCode != 404 {
				t.Errorf("%s: page %q: status %d, want 404", v.File, n, res.StatusCode)
			}
		}
		if res := req(t, "GET", base+"/api/jobs/"+id+"/pages/0", nil, nil); res.StatusCode != 401 {
			t.Errorf("%s: a page was served without the token: %d", v.File, res.StatusCode)
		}
	}

	all := req(t, "GET", base+"/api/download-all?t="+s.token, nil, nil)
	zb, _ := io.ReadAll(all.Body)
	zr, err := zip.NewReader(bytes.NewReader(zb), int64(len(zb)))
	if err != nil || len(zr.File) != 2 {
		t.Fatalf("download-all: %v, %d files", err, len(zr.File))
	}

	if res := req(t, "DELETE", base+"/api/jobs/"+ids[0], nil, map[string]string{"X-Token": s.token}); res.StatusCode != 204 {
		t.Errorf("delete: status %d", res.StatusCode)
	}
	if res := req(t, "GET", base+"/api/jobs/"+ids[0]+"/epub?t="+s.token, nil, nil); res.StatusCode != 404 {
		t.Errorf("a removed job's EPUB is still served: %d", res.StatusCode)
	}
}

func TestGUIRejectsNonPDF(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the PDF engine; skipped with -short")
	}
	s, base := startGUI(t, true)
	ch := events(t, s, base)
	<-ch
	v := upload(t, s, base, map[string][]byte{"notes.pdf": []byte("not a pdf")})
	got := waitJobs(t, ch, stateFailed, v[0].ID)
	if got[v[0].ID].Error == "" {
		t.Error("a file that is not a PDF should fail with a message")
	}
}

// Settings saved by an older version have no contents choice; they get the
// default, and an unknown choice is refused.
func TestGUISettingsContents(t *testing.T) {
	s := defaultSettings()
	s.TOC = ""
	if o, err := s.options("t", "in.pdf", "out.epub", 1); err != nil || o.TOC != tocBookmarks {
		t.Errorf("empty contents setting: %q, %v", o.TOC, err)
	}
	s.TOC = tocPages
	if o, err := s.options("t", "in.pdf", "out.epub", 1); err != nil || o.TOC != tocPages {
		t.Errorf("pages: %q, %v", o.TOC, err)
	}
	s.TOC = "chapters"
	if _, err := s.options("t", "in.pdf", "out.epub", 1); err == nil {
		t.Error("an unknown contents setting was accepted")
	}
}

// A password-protected PDF waits for its password, and a page range
// converts only the pages asked for.
func TestGUIPasswordAndRange(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the PDF engine; skipped with -short")
	}
	s, base := startGUI(t, true)
	ch := events(t, s, base)
	<-ch // snapshot

	pages := []testPage{{W: 400, H: 600}, {W: 400, H: 600}, {W: 400, H: 600}, {W: 400, H: 600}}
	views := upload(t, s, base, map[string][]byte{"locked.pdf": makePDFWith(pages, testPDF{Password: "s3cret"})})
	id := views[0].ID
	if v := waitJobs(t, ch, stateFailed, id)[id]; !v.Locked || v.Error != errPasswordNeeded.Error() {
		t.Fatalf("locked PDF inspected as %+v", v)
	}

	tok := map[string]string{"X-Token": s.token}
	unlock := func(pw string) int {
		b, _ := json.Marshal(map[string]string{"password": pw})
		return req(t, "POST", base+"/api/jobs/"+id+"/unlock", bytes.NewReader(b), tok).StatusCode
	}
	if code := unlock("wrong"); code != 202 {
		t.Fatalf("unlock: status %d", code)
	}
	if v := waitJobs(t, ch, stateFailed, id)[id]; !v.Locked || v.Error != errPasswordWrong.Error() {
		t.Fatalf("after a wrong password: %+v", v)
	}
	unlock("s3cret")
	if v := waitJobs(t, ch, stateReady, id)[id]; v.Pages != 4 || v.Locked {
		t.Fatalf("after the right password: %+v", v)
	}
	if code := unlock("again"); code != 409 {
		t.Errorf("unlocking an open file: status %d, want 409", code)
	}

	// A range beyond the end is refused; a good one is converted.
	convert := func(r string) int {
		b, _ := json.Marshal(convertRequest{IDs: []string{id}, Settings: defaultSettings(), Pages: map[string]string{id: r}})
		return req(t, "POST", base+"/api/convert", bytes.NewReader(b), tok).StatusCode
	}
	if code := convert("3-9"); code != 400 {
		t.Errorf("range beyond the end: status %d, want 400", code)
	}
	if code := convert("2-3"); code != 202 {
		t.Fatalf("convert: status %d", code)
	}
	v := waitJobs(t, ch, stateDone, id)[id]
	if r := v.Result; r == nil || r.Pages != 2 || r.Of != 4 || !r.Passed || v.Range != "2-3" {
		t.Fatalf("converted as %+v, result %+v", v, v.Result)
	}
	s.mu.Lock()
	b, _ := json.Marshal(s.all[id].view)
	s.mu.Unlock()
	if bytes.Contains(b, []byte("s3cret")) {
		t.Error("the password is sent to the page")
	}
}

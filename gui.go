package main

// The graphical interface: a local web app served from inside the binary.
//
// Native Go GUI toolkits all need cgo and a C toolchain per platform, which
// would end the single static, cross-compiled binary. Instead the program
// serves its interface on the loopback interface and opens it in a browser,
// as a chromeless app window when Chrome or Edge is installed.
//
// Security: the server listens on 127.0.0.1 only. Every API call needs a
// random per-session token, requests naming any other Host are refused
// (DNS rebinding), and state-changing requests must carry the token in a
// header, which a cross-site form cannot set.

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web
var webFiles embed.FS

// guiOptions configure the GUI.
type guiOptions struct {
	NoBrowser bool   // print the address instead of opening a window
	Addr      string // listen address; tests use 127.0.0.1:0
	idleExit  time.Duration
}

const maxUpload = 2 << 30 // 2 GiB per request

// jobSettings are the conversion options the interface can set.
type jobSettings struct {
	Lang        string `json:"lang"`
	Direction   string `json:"direction"`
	Grayscale   bool   `json:"grayscale"`
	FlattenBG   bool   `json:"flattenBG"`
	DPI         int    `json:"dpi"` // 0 = automatic
	Quality     int    `json:"quality"`
	MaxEdge     int    `json:"maxEdge"`
	Orientation string `json:"orientation"` // "" = automatic
	Mixed       bool   `json:"mixed"`
	TOC         string `json:"toc"` // "" = bookmarks
	Validate    bool   `json:"validate"`
}

func defaultSettings() jobSettings {
	d := defaultOptions()
	return jobSettings{
		Lang: d.Lang, Direction: d.Direction, Grayscale: true, DPI: d.DPI,
		Quality: d.Quality, MaxEdge: d.MaxEdge, TOC: d.TOC, Validate: true,
	}
}

// options turns settings into validated conversion options.
func (s jobSettings) options(title, in, out string, jobs int) (Options, error) {
	o := defaultOptions()
	o.Input, o.Output, o.Title, o.Jobs = in, out, title, jobs
	switch {
	case !validLang(s.Lang):
		return o, fmt.Errorf("%q is not a valid language tag", s.Lang)
	case s.Direction != "ltr" && s.Direction != "rtl":
		return o, errors.New("direction must be ltr or rtl")
	case s.DPI != dpiAuto && (s.DPI < 18 || s.DPI > 1200):
		return o, errors.New("resolution must be automatic or 18 to 1200 DPI")
	case s.Quality < 1 || s.Quality > 100:
		return o, errors.New("quality must be 1 to 100")
	case s.MaxEdge < 0 || s.MaxEdge > 20000:
		return o, errors.New("maximum edge must be 0 to 20000 pixels")
	}
	switch s.Orientation {
	case "", "portrait", "landscape", "auto", "none":
	default:
		return o, errors.New("orientation must be automatic, portrait or landscape")
	}
	switch s.TOC {
	case "":
		s.TOC = tocBookmarks
	case tocBookmarks, tocPages:
	default:
		return o, errors.New("contents must be bookmarks or pages")
	}
	if strings.TrimSpace(title) == "" {
		return o, errors.New("the title is empty")
	}
	o.Lang, o.Direction, o.Grayscale, o.FlattenBG = s.Lang, s.Direction, s.Grayscale, s.FlattenBG
	o.DPI, o.Quality, o.MaxEdge, o.Orient, o.Mixed = s.DPI, s.Quality, s.MaxEdge, s.Orientation, s.Mixed
	o.TOC, o.Validate, o.Epubcheck = s.TOC, s.Validate, s.Validate
	return o, nil
}

type jobState string

const (
	stateInspecting jobState = "inspecting"
	stateReady      jobState = "ready"
	stateQueued     jobState = "queued"
	stateConverting jobState = "converting"
	stateDone       jobState = "done"
	stateFailed     jobState = "failed"
)

// jobView is what the page sees of a job.
type jobView struct {
	ID       string       `json:"id"`
	File     string       `json:"file"`
	Title    string       `json:"title"`
	Bytes    int64        `json:"bytes"`
	Pages    int          `json:"pages,omitempty"`
	Orient   string       `json:"orientation,omitempty"`
	ScanPPI  int          `json:"scanPPI,omitempty"`
	State    jobState     `json:"state"`
	Stage    string       `json:"stage,omitempty"`
	Done     int          `json:"done"`
	Total    int          `json:"total"`
	HasCover bool         `json:"hasCover"`
	Locked   bool         `json:"locked,omitempty"`    // needs a password to open
	Range    string       `json:"pageRange,omitempty"` // pages to convert; "" is all
	Result   *resultView  `json:"result,omitempty"`
	Error    string       `json:"error,omitempty"`
	Settings *jobSettings `json:"settings,omitempty"`
	Seq      int64        `json:"seq"`
}

type codeCount struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

type resultView struct {
	Pages       int         `json:"pages"`
	Of          int         `json:"of"` // pages in the PDF
	Canvas      string      `json:"canvas"`
	Orientation string      `json:"orientation"`
	DPI         int         `json:"dpi"`
	Scan        bool        `json:"scan"`
	Bytes       int         `json:"bytes"`
	Seconds     float64     `json:"seconds"`
	Flattened   int         `json:"flattened"`
	TOC         int         `json:"toc"` // entries from the PDF's bookmarks
	Validated   bool        `json:"validated"`
	Passed      bool        `json:"passed"`
	Codes       []codeCount `json:"codes,omitempty"`
	Problems    []string    `json:"problems,omitempty"`
	Epubcheck   string      `json:"epubcheck"` // passed, failed, missing, skipped
	EpubSummary string      `json:"epubcheckSummary,omitempty"`
	Built       int64       `json:"built"` // distinguishes one conversion's pages from the next
}

type job struct {
	view     jobView
	pdf      string
	epub     string
	cover    []byte
	password string       // for an encrypted PDF; kept in memory only
	book     *previewBook // read back from the EPUB on first preview
	cancel   context.CancelFunc
	lastPub  time.Time
}

type guiServer struct {
	token  string
	origin string // http://127.0.0.1:port
	dir    string
	jobs   int // pages rendered in parallel

	ctx     context.Context
	engOnce sync.Once
	eng     *engine
	engErr  error
	engDone chan struct{}

	mu    sync.Mutex
	all   map[string]*job
	order []string
	seq   int64
	queue chan string
	subs  map[chan []byte]struct{}
	seen  bool      // a page has connected at least once
	idle  time.Time // when the last page disconnected

	epubcheck bool
	quit      chan struct{}
	quitOnce  sync.Once
}

// runGUI serves the interface until the window is closed, Quit is chosen, or
// ctx is cancelled.
func runGUI(ctx context.Context, g guiOptions, out io.Writer) error {
	addr := g.Addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("starting the interface: %w", err)
	}
	s, err := newGUIServer(ctx, ln.Addr().String())
	if err != nil {
		ln.Close()
		return err
	}
	defer os.RemoveAll(s.dir)

	srv := &http.Server{Handler: s.handler(), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	go s.runQueue()
	go s.warmEngine()

	link := s.origin + "/?t=" + s.token
	fmt.Fprintf(out, "Leafbind is running at\n\n    %s\n\nClose its window or press Ctrl+C to quit.\n", link)
	if !g.NoBrowser {
		if err := openUI(link); err != nil {
			fmt.Fprintf(out, "Could not open a browser (%v); open the address above.\n", err)
		}
		detachConsole() // Windows: close the console of a double-clicked program
	}

	idle := g.idleExit
	if idle == 0 {
		idle = 20 * time.Second
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-s.quit:
			break loop
		case <-tick.C:
			// Exit once the window has been closed: a page connected, and none
			// has been connected for a while. Reloads reconnect within a second.
			s.mu.Lock()
			gone := s.seen && len(s.subs) == 0 && time.Since(s.idle) > idle && !g.NoBrowser
			s.mu.Unlock()
			if gone {
				break loop
			}
		}
	}

	s.mu.Lock()
	for _, j := range s.all {
		if j.cancel != nil {
			j.cancel()
		}
	}
	s.mu.Unlock()
	shut, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	srv.Shutdown(shut)
	if s.eng != nil {
		s.eng.Close()
	}
	return nil
}

func newGUIServer(ctx context.Context, hostport string) (*guiServer, error) {
	var t [24]byte
	if _, err := rand.Read(t[:]); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "leafbind-gui-*")
	if err != nil {
		return nil, err
	}
	_, lookErr := exec.LookPath("epubcheck")
	return &guiServer{
		token:     hex.EncodeToString(t[:]),
		origin:    "http://" + hostport,
		dir:       dir,
		jobs:      defaultOptions().Jobs,
		ctx:       ctx,
		engDone:   make(chan struct{}),
		all:       map[string]*job{},
		queue:     make(chan string, 1024),
		subs:      map[chan []byte]struct{}{},
		epubcheck: lookErr == nil,
		quit:      make(chan struct{}),
	}, nil
}

// warmEngine starts the PDF engine in the background, so the few seconds it
// takes to compile are spent while the user is still choosing files.
func (s *guiServer) warmEngine() {
	s.engOnce.Do(func() {
		// One instance more than a conversion uses, so a newly added file can
		// be inspected while another converts.
		s.eng, s.engErr = newEngine(s.ctx, s.jobs+1)
		close(s.engDone)
		s.broadcastInfo()
	})
}

func (s *guiServer) engine() (*engine, error) {
	go s.warmEngine()
	select {
	case <-s.engDone:
		return s.eng, s.engErr
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}

func (s *guiServer) engineReady() bool {
	select {
	case <-s.engDone:
		return s.engErr == nil
	default:
		return false
	}
}

func (s *guiServer) handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFiles, "web")
	files := http.FileServerFS(static)

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		b, _ := webFiles.ReadFile("web/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(b)
	})
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", files))

	api := http.NewServeMux()
	api.HandleFunc("GET /api/events", s.events)
	api.HandleFunc("POST /api/files", s.upload)
	api.HandleFunc("POST /api/convert", s.convert)
	api.HandleFunc("PATCH /api/jobs/{id}", s.rename)
	api.HandleFunc("POST /api/jobs/{id}/unlock", s.unlock)
	api.HandleFunc("DELETE /api/jobs/{id}", s.remove)
	api.HandleFunc("GET /api/jobs/{id}/epub", s.download)
	api.HandleFunc("GET /api/jobs/{id}/cover", s.coverImage)
	api.HandleFunc("GET /api/jobs/{id}/preview", s.preview)
	api.HandleFunc("GET /api/jobs/{id}/pages/{n}", s.pageImage)
	api.HandleFunc("GET /api/download-all", s.downloadAll)
	api.HandleFunc("GET /api/licenses", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, licenseText+"\n"+noticesText)
	})
	api.HandleFunc("POST /api/quit", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
		s.quitOnce.Do(func() { close(s.quit) })
	})
	mux.Handle("/api/", s.guard(api))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Refuse any Host but our own, which defeats DNS rebinding.
		if r.Host != strings.TrimPrefix(s.origin, "http://") && r.Host != "localhost"+s.port() {
			http.Error(w, "unexpected host", http.StatusForbidden)
			return
		}
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' blob: data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		mux.ServeHTTP(w, r)
	})
}

func (s *guiServer) port() string {
	_, p, _ := strings.Cut(strings.TrimPrefix(s.origin, "http://"), ":")
	return ":" + p
}

// guard checks the session token. Reads may pass it as ?t= (an EventSource
// or a download link cannot set headers); writes must use the X-Token header
// and, if the browser sends an Origin, it must be ours.
func (s *guiServer) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := r.Header.Get("X-Token")
		if r.Method == http.MethodGet && tok == "" {
			tok = r.URL.Query().Get("t")
		}
		if subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) != 1 {
			http.Error(w, "missing or wrong session token", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			if r.Header.Get("X-Token") == "" {
				http.Error(w, "token must be sent in the X-Token header", http.StatusUnauthorized)
				return
			}
			if o := r.Header.Get("Origin"); o != "" && o != s.origin && o != "http://localhost"+s.port() {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// ----- events ---------------------------------------------------------------

type infoView struct {
	Version     string      `json:"version"`
	Epubcheck   bool        `json:"epubcheck"`
	EngineReady bool        `json:"engineReady"`
	EngineError string      `json:"engineError,omitempty"`
	Defaults    jobSettings `json:"defaults"`
	OS          string      `json:"os"`
}

func (s *guiServer) info() infoView {
	v := infoView{Version: version, Epubcheck: s.epubcheck, EngineReady: s.engineReady(), Defaults: defaultSettings(), OS: runtime.GOOS}
	select {
	case <-s.engDone:
		if s.engErr != nil {
			v.EngineError = s.engErr.Error()
		}
	default:
	}
	return v
}

func sse(event string, v any) []byte {
	b, _ := json.Marshal(v)
	return []byte("event: " + event + "\ndata: " + string(b) + "\n\n")
}

func (s *guiServer) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := make(chan []byte, 256)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.seen = true
	jobs := make([]jobView, 0, len(s.order))
	for _, id := range s.order {
		jobs = append(jobs, s.all[id].view)
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.idle = time.Now()
		s.mu.Unlock()
	}()

	w.Write(sse("snapshot", map[string]any{"info": s.info(), "jobs": jobs}))
	fl.Flush()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.quit:
			w.Write([]byte("event: quit\ndata: {}\n\n"))
			fl.Flush()
			return
		case msg := <-ch:
			w.Write(msg)
			fl.Flush()
		case <-ping.C:
			w.Write([]byte(": ping\n\n"))
			fl.Flush()
		}
	}
}

func (s *guiServer) broadcast(msg []byte) {
	for ch := range s.subs {
		select {
		case ch <- msg:
		default: // a stalled page misses an update; the next one supersedes it
		}
	}
}

func (s *guiServer) broadcastInfo() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.broadcast(sse("info", s.info()))
}

// publish sends a job's view; call with s.mu held. Progress updates are
// limited to ten a second per job, but a change of state always goes out.
func (s *guiServer) publish(j *job, force bool) {
	if !force && time.Since(j.lastPub) < 100*time.Millisecond {
		return
	}
	s.seq++
	j.view.Seq = s.seq
	j.lastPub = time.Now()
	s.broadcast(sse("job", j.view))
}

// ----- files and jobs --------------------------------------------------------

func newID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (s *guiServer) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "expected a multipart upload", http.StatusBadRequest)
		return
	}
	var added []jobView
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			http.Error(w, "reading the upload: "+err.Error(), http.StatusBadRequest)
			return
		}
		if part.FormName() != "file" {
			continue
		}
		name := filepath.Base(strings.ReplaceAll(part.FileName(), `\`, "/"))
		if name == "" || name == "." || name == "/" {
			name = "document.pdf"
		}
		id := newID()
		pdf := filepath.Join(s.dir, id+".pdf")
		f, err := os.Create(pdf)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		n, err := io.Copy(f, part)
		f.Close()
		if err != nil {
			os.Remove(pdf)
			http.Error(w, "receiving "+name+": "+err.Error(), http.StatusBadRequest)
			return
		}

		j := &job{pdf: pdf, epub: filepath.Join(s.dir, id+".epub")}
		j.view = jobView{
			ID: id, File: name, Bytes: n, State: stateInspecting,
			Title: strings.TrimSuffix(name, filepath.Ext(name)),
		}
		s.mu.Lock()
		s.all[id] = j
		s.order = append(s.order, id)
		s.publish(j, true)
		added = append(added, j.view)
		s.mu.Unlock()
		go s.inspect(id)
	}
	if len(added) == 0 {
		http.Error(w, "no files in the upload", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, added)
}

// inspect reads a new file's page count, first-page orientation and whether
// it is a scan, and renders a cover preview, before it is converted.
func (s *guiServer) inspect(id string) {
	s.mu.Lock()
	j, ok := s.all[id]
	s.mu.Unlock()
	if !ok {
		return
	}
	fail := func(err error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		j.view.State, j.view.Error = stateFailed, err.Error()
		j.view.Locked = errors.Is(err, errPasswordNeeded) || errors.Is(err, errPasswordWrong)
		s.publish(j, true)
	}

	eng, err := s.engine()
	if err != nil {
		fail(fmt.Errorf("the PDF engine did not start: %w", err))
		return
	}
	data, err := os.ReadFile(j.pdf)
	if err != nil {
		fail(err)
		return
	}
	s.mu.Lock()
	password := j.password
	s.mu.Unlock()
	wk, err := eng.newWorker(data, password)
	if err != nil {
		fail(err)
		return
	}
	defer wk.Close()
	n, err := wk.pageCount()
	if err == nil && n == 0 {
		err = errors.New("the PDF has no pages")
	}
	if err != nil {
		fail(err)
		return
	}
	first, err := wk.pageSize(0, 72)
	if err != nil {
		fail(err)
		return
	}
	ppi, scan := wk.scanPPI(allPages(n))
	// Render the cover at about the 360 pixels wide the preview needs.
	var cover []byte
	if img, err := wk.render(0, max(8, min(150, 360*72/max(1, first.W)))); err == nil {
		cover = thumbnail(img, 360)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if j.view.State != stateInspecting {
		return // removed meanwhile
	}
	j.view.Pages, j.view.Orient = n, first.Orientation()
	if scan {
		j.view.ScanPPI = ppi
	}
	if cover != nil {
		j.cover, j.view.HasCover = cover, true
	}
	j.view.State = stateReady
	s.publish(j, true)
}

type convertRequest struct {
	IDs      []string          `json:"ids"`
	Settings jobSettings       `json:"settings"`
	Titles   map[string]string `json:"titles"`
	Pages    map[string]string `json:"pages"` // page range per file; missing or "" is all
}

func (s *guiServer) convert(w http.ResponseWriter, r *http.Request) {
	var req convertRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Validate everything before queueing anything.
	for _, id := range req.IDs {
		j, ok := s.all[id]
		if !ok {
			http.Error(w, "unknown file "+id, http.StatusNotFound)
			return
		}
		title := j.view.Title
		if t, ok := req.Titles[id]; ok {
			title = strings.TrimSpace(t)
		}
		if _, err := req.Settings.options(title, j.pdf, j.epub, s.jobs); err != nil {
			http.Error(w, j.view.File+": "+err.Error(), http.StatusBadRequest)
			return
		}
		if r := strings.TrimSpace(req.Pages[id]); r != "" && j.view.Pages > 0 {
			spans, err := parsePages(r)
			if err == nil {
				_, err = selectPages(spans, j.view.Pages)
			}
			if err != nil {
				http.Error(w, j.view.File+": pages: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
	}
	queued := 0
	for _, id := range req.IDs {
		j := s.all[id]
		switch j.view.State {
		case stateReady, stateDone, stateFailed:
		default:
			continue // inspecting, or already queued or converting
		}
		if j.view.Pages == 0 { // a file that failed inspection cannot convert
			continue
		}
		if t, ok := req.Titles[id]; ok {
			j.view.Title = strings.TrimSpace(t)
		}
		st := req.Settings
		j.view.Settings = &st
		j.view.Range = strings.TrimSpace(req.Pages[id])
		j.view.State, j.view.Error, j.view.Result = stateQueued, "", nil
		j.view.Stage, j.view.Done, j.view.Total = "", 0, 0
		s.publish(j, true)
		s.queue <- id
		queued++
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"queued": queued})
}

// unlock retries opening an encrypted PDF with a password.
func (s *guiServer) unlock(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil || body.Password == "" {
		http.Error(w, "a password is required", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	s.mu.Lock()
	j, ok := s.all[id]
	if !ok || !j.view.Locked || j.view.State != stateFailed {
		s.mu.Unlock()
		http.Error(w, "that file is not waiting for a password", http.StatusConflict)
		return
	}
	j.password = body.Password
	j.view.State, j.view.Error, j.view.Locked = stateInspecting, "", false
	s.publish(j, true)
	s.mu.Unlock()
	go s.inspect(id)
	w.WriteHeader(http.StatusAccepted)
}

func (s *guiServer) rename(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil || strings.TrimSpace(body.Title) == "" {
		http.Error(w, "a non-empty title is required", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.all[r.PathValue("id")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	j.view.Title = strings.TrimSpace(body.Title)
	s.publish(j, true)
	w.WriteHeader(http.StatusNoContent)
}

func (s *guiServer) remove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	j, ok := s.all[id]
	if ok {
		if j.cancel != nil {
			j.cancel()
		}
		delete(s.all, id)
		for i, o := range s.order {
			if o == id {
				s.order = append(s.order[:i], s.order[i+1:]...)
				break
			}
		}
		s.broadcast(sse("removed", map[string]string{"id": id}))
	}
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	// A running conversion may still be writing; remove its files once it
	// has had a moment to notice the cancellation.
	go func() {
		time.Sleep(2 * time.Second)
		os.Remove(j.pdf)
		os.Remove(j.epub)
	}()
	w.WriteHeader(http.StatusNoContent)
}

// runQueue converts queued jobs one at a time; each already renders its pages
// in parallel.
func (s *guiServer) runQueue() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.quit:
			return
		case id := <-s.queue:
			s.runJob(id)
		}
	}
}

func (s *guiServer) runJob(id string) {
	s.mu.Lock()
	j, ok := s.all[id]
	if !ok || j.view.State != stateQueued {
		s.mu.Unlock()
		return
	}
	o, err := j.view.Settings.options(j.view.Title, j.pdf, j.epub, s.jobs)
	o.Pages, o.Password = j.view.Range, j.password
	ctx, cancel := context.WithCancel(s.ctx)
	j.cancel = cancel
	j.book = nil
	j.view.State = stateConverting
	s.publish(j, true)
	s.mu.Unlock()
	defer cancel()

	var res *Result
	if err == nil {
		var eng *engine
		if eng, err = s.engine(); err == nil {
			res, err = convertBook(ctx, eng, o, func(e Event) {
				s.mu.Lock()
				defer s.mu.Unlock()
				changed := j.view.Stage != e.Stage
				j.view.Stage, j.view.Done, j.view.Total = e.Stage, e.Done, max(e.Total, j.view.Total)
				s.publish(j, changed)
			})
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	j.cancel = nil
	if _, still := s.all[id]; !still {
		return // removed while converting
	}
	if err != nil {
		if ctx.Err() != nil {
			err = errors.New("cancelled")
		}
		j.view.State, j.view.Error = stateFailed, err.Error()
		s.publish(j, true)
		return
	}
	j.view.State, j.view.Stage = stateDone, ""
	j.view.Result = newResultView(res, o)
	if res.Cover != nil {
		j.cover, j.view.HasCover = res.Cover, true
	}
	s.publish(j, true)
}

func newResultView(r *Result, o Options) *resultView {
	v := &resultView{
		Pages: r.Pages, Of: r.Of, Orientation: r.Orient.Book, DPI: r.DPI, Scan: r.Scan,
		Bytes: r.Bytes, Seconds: r.Elapsed.Seconds(), Flattened: r.Flattened, TOC: r.TOC,
		Validated: r.Validated, Passed: r.Passed, Epubcheck: "skipped",
		Built: time.Now().UnixNano(),
	}
	v.Canvas = fmt.Sprintf("%d × %d", r.Canvas.W, r.Canvas.H)
	if o.Mixed {
		v.Canvas = "per page"
	}
	var lines []string
	for _, p := range r.Problems {
		lines = append(lines, p.String())
	}
	if r.Validated && o.Epubcheck {
		switch ec := r.Epubcheck; {
		case !ec.Ran:
			v.Epubcheck = "missing"
		case ec.Passed:
			v.Epubcheck, v.EpubSummary = "passed", ec.Summary
		default:
			v.Epubcheck, v.EpubSummary = "failed", ec.Summary
			lines = append(lines, ec.Problems...)
		}
	}
	codes, by := problemCounts(lines)
	for _, c := range codes {
		v.Codes = append(v.Codes, codeCount{c, by[c]})
	}
	v.Problems = lines[:min(len(lines), 20)]
	return v
}

// ----- downloads -------------------------------------------------------------

func epubName(file string) string {
	return strings.TrimSuffix(file, filepath.Ext(file)) + ".epub"
}

// attachment sets a download filename that survives any characters: a plain
// ASCII fallback plus the exact name in RFC 5987 form.
func attachment(w http.ResponseWriter, name string) {
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || strings.ContainsRune(`"\/:*?<>|`, r) {
			return '_'
		}
		return r
	}, name)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": ascii})+
		"; filename*=UTF-8''"+url.PathEscape(name))
}

func (s *guiServer) finished(id string) (*job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.all[id]
	return j, ok && j.view.State == stateDone
}

func (s *guiServer) download(w http.ResponseWriter, r *http.Request) {
	j, ok := s.finished(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(j.epub)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/epub+zip")
	attachment(w, epubName(j.view.File))
	http.ServeContent(w, r, "", time.Time{}, f)
}

func (s *guiServer) coverImage(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	j, ok := s.all[r.PathValue("id")]
	var c []byte
	if ok {
		c = j.cover
	}
	s.mu.Unlock()
	if c == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(c)))
	w.Write(c)
}

// ----- preview ---------------------------------------------------------------

// previewOf reads a finished job's EPUB back, once, for the preview.
func (s *guiServer) previewOf(id string) (*job, *previewBook, error) {
	j, ok := s.finished(id)
	if !ok {
		return nil, nil, errNotFinished
	}
	s.mu.Lock()
	b := j.book
	s.mu.Unlock()
	if b != nil {
		return j, b, nil
	}
	zr, err := zip.OpenReader(j.epub)
	if err != nil {
		return nil, nil, err
	}
	defer zr.Close()
	if b, err = readPreview(&zr.Reader); err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	if j.view.State == stateDone {
		j.book = b
	}
	s.mu.Unlock()
	return j, b, nil
}

var errNotFinished = errors.New("the book is not finished")

func (s *guiServer) preview(w http.ResponseWriter, r *http.Request) {
	_, b, err := s.previewOf(r.PathValue("id"))
	switch {
	case errors.Is(err, errNotFinished):
		http.NotFound(w, r)
	case err != nil:
		http.Error(w, "the EPUB cannot be previewed: "+err.Error(), http.StatusUnprocessableEntity)
	default:
		writeJSON(w, http.StatusOK, b)
	}
}

func (s *guiServer) pageImage(w http.ResponseWriter, r *http.Request) {
	j, b, err := s.previewOf(r.PathValue("id"))
	n, nerr := strconv.Atoi(r.PathValue("n"))
	if err != nil || nerr != nil || n < 0 || n >= len(b.Pages) {
		http.NotFound(w, r)
		return
	}
	zr, err := zip.OpenReader(j.epub)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer zr.Close()
	data, typ, err := previewImage(&zr.Reader, b, n)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// The page asks for each conversion's pages under a new URL, so they
	// can be cached.
	w.Header().Set("Content-Type", typ)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Write(data)
}

// downloadAll streams every finished EPUB in one ZIP, stored rather than
// compressed, since EPUBs are compressed already.
func (s *guiServer) downloadAll(w http.ResponseWriter, r *http.Request) {
	type item struct{ path, name string }
	var items []item
	s.mu.Lock()
	for _, id := range s.order {
		if j := s.all[id]; j.view.State == stateDone {
			items = append(items, item{j.epub, epubName(j.view.File)})
		}
	}
	s.mu.Unlock()
	if len(items) == 0 {
		http.NotFound(w, r)
		return
	}
	// Two sources can share a file name; number the later ones.
	seen := map[string]int{}
	for i := range items {
		n := items[i].name
		if k := seen[n]; k > 0 {
			ext := filepath.Ext(n)
			items[i].name = fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(n, ext), k+1, ext)
		}
		seen[n]++
	}

	w.Header().Set("Content-Type", "application/zip")
	attachment(w, "ebooks.zip")
	zw := zip.NewWriter(w)
	for _, it := range items {
		f, err := os.Open(it.path)
		if err != nil {
			continue
		}
		dst, err := zw.CreateHeader(&zip.FileHeader{Name: it.name, Method: zip.Store, Modified: time.Now()})
		if err == nil {
			io.Copy(dst, f)
		}
		f.Close()
	}
	zw.Close()
}

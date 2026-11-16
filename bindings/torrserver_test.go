package torrServer

import (
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

var client = &http.Client{Timeout: 30 * time.Second}

func freePort(t *testing.T) int {
	t.Helper()
	for {
		l, err := net.Listen("tcp4", ":0")
		if err != nil {
			t.Fatal(err)
		}
		p := l.Addr().(*net.TCPAddr).Port
		l.Close()
		u, err := net.ListenPacket("udp4", ":"+strconv.Itoa(p))
		if err == nil {
			u.Close()
			return p
		}
	}
}

type hold struct {
	l net.Listener
	u net.PacketConn
}

func grab(p int) (*hold, error) {
	l, err := net.Listen("tcp4", ":"+strconv.Itoa(p))
	if err != nil {
		return nil, err
	}
	u, err := net.ListenPacket("udp4", ":"+strconv.Itoa(p))
	if err != nil {
		l.Close()
		return nil, err
	}
	return &hold{l, u}, nil
}

func (h *hold) release() {
	h.l.Close()
	h.u.Close()
}

func start(t *testing.T, dir string) int {
	t.Helper()
	p := StartTorrentServer(dir, 0)
	if p <= 0 {
		t.Fatalf("start failed: %d", p)
	}
	return p
}

func startEngine(t *testing.T) int {
	t.Helper()
	dir := t.TempDir()
	p := start(t, dir)
	t.Cleanup(StopTorrentServer)
	return p
}

func url(port int, path string) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", port, path)
}

func postRaw(t *testing.T, port int, path string, body any) (int, []byte) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Post(url(port, path), "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func postOK(t *testing.T, port int, path string, body any) []byte {
	t.Helper()
	code, data := postRaw(t, port, path, body)
	if code != http.StatusOK {
		t.Fatalf("POST %s %v: status %d %s", path, body, code, data)
	}
	return data
}

func post(port int, body any) (map[string]any, error) {
	b, _ := json.Marshal(body)
	resp, err := client.Post(url(port, "/settings"), "application/json", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d %s", resp.StatusCode, data)
	}
	var m map[string]any
	if len(data) > 0 {
		json.Unmarshal(data, &m)
	}
	return m, nil
}

func getSettings(t *testing.T, port int) map[string]any {
	t.Helper()
	m, err := post(port, map[string]any{"action": "get"})
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("settings get returned no object")
	}
	return m
}

func setPeerPort(t *testing.T, port, peer int) {
	sets, err := post(port, map[string]any{"action": "get"})
	if err != nil {
		t.Fatal(err)
	}
	sets["PeersListenPort"] = peer
	sets["DisableUPNP"] = true
	sets["DisableDHT"] = true
	sets["EnableLPD"] = false
	go post(port, map[string]any{"action": "set", "sets": sets})
}

func echo(t *testing.T, port int) string {
	t.Helper()
	resp, err := client.Get(url(port, "/echo"))
	if err != nil {
		t.Fatalf("echo: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("echo status %d", resp.StatusCode)
	}
	return string(b)
}

func bencode(w *bytes.Buffer, v any) {
	switch x := v.(type) {
	case int:
		fmt.Fprintf(w, "i%de", x)
	case string:
		fmt.Fprintf(w, "%d:%s", len(x), x)
	case []byte:
		fmt.Fprintf(w, "%d:", len(x))
		w.Write(x)
	case []any:
		w.WriteByte('l')
		for _, e := range x {
			bencode(w, e)
		}
		w.WriteByte('e')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		w.WriteByte('d')
		for _, k := range keys {
			bencode(w, k)
			bencode(w, x[k])
		}
		w.WriteByte('e')
	default:
		panic(fmt.Sprintf("bencode %T", v))
	}
}

type testFile struct {
	path   []string
	length int
}

func makeTorrent(t *testing.T, name string, files []testFile) (string, string) {
	t.Helper()
	const pieceLength = 16384
	total := 0
	var list []any
	for _, f := range files {
		p := make([]any, len(f.path))
		for i, s := range f.path {
			p[i] = s
		}
		list = append(list, map[string]any{"length": f.length, "path": p})
		total += f.length
	}
	var pieces []byte
	zero := make([]byte, pieceLength)
	for off := 0; off < total; off += pieceLength {
		n := min(pieceLength, total-off)
		sum := sha1.Sum(zero[:n])
		pieces = append(pieces, sum[:]...)
	}
	info := map[string]any{
		"name":         name,
		"piece length": pieceLength,
		"pieces":       pieces,
		"files":        list,
	}
	var ib bytes.Buffer
	bencode(&ib, info)
	hash := sha1.Sum(ib.Bytes())
	var tb bytes.Buffer
	tb.WriteString("d4:info")
	tb.Write(ib.Bytes())
	tb.WriteString("e")
	path := filepath.Join(t.TempDir(), name+".torrent")
	if err := os.WriteFile(path, tb.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path, hex.EncodeToString(hash[:])
}

func randomHash(t *testing.T) string {
	t.Helper()
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

type fileStat struct {
	ID     int    `json:"id"`
	Path   string `json:"path"`
	Length int64  `json:"length"`
}

type status struct {
	Hash       string     `json:"hash"`
	Title      string     `json:"title"`
	Stat       *int       `json:"stat"`
	StatString string     `json:"stat_string"`
	FileStats  []fileStat `json:"file_stats"`
}

func decodeStatus(t *testing.T, data []byte) status {
	t.Helper()
	var s status
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("status %s: %v", data, err)
	}
	return s
}

func listHashes(t *testing.T, port int) []string {
	t.Helper()
	data := postOK(t, port, "/torrents", map[string]any{"action": "list"})
	var list []status
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("list %s: %v", data, err)
	}
	if list == nil {
		t.Fatalf("list must be a JSON array, got %s", data)
	}
	var hashes []string
	for _, s := range list {
		hashes = append(hashes, s.Hash)
	}
	return hashes
}

func contains(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

func TestStartReturnsRealLoopbackPort(t *testing.T) {
	port := startEngine(t)
	if port <= 0 || port > 65535 {
		t.Fatalf("port %d", port)
	}
	if again := StartTorrentServer(t.TempDir(), 0); again != port {
		t.Fatalf("second start while running returned %d, want %d", again, port)
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 2*time.Second)
	if err != nil {
		t.Fatalf("dial loopback: %v", err)
	}
	c.Close()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.IsLoopback() || ipn.IP.IsLinkLocalUnicast() {
			continue
		}
		c, err := net.DialTimeout("tcp", net.JoinHostPort(ipn.IP.String(), strconv.Itoa(port)), 2*time.Second)
		if err == nil {
			c.Close()
			t.Errorf("engine reachable on %s:%d, want 127.0.0.1 only", ipn.IP, port)
		}
		checked++
	}
	if checked == 0 {
		t.Log("no non-loopback interface address to check")
	}
}

func TestEchoIsExactSignature(t *testing.T) {
	port := startEngine(t)
	if got, want := echo(t, port), "ServeTorr torrserver "+tag; got != want {
		t.Fatalf("echo %q, want %q", got, want)
	}
}

func TestBrowserRequestsAreRejected(t *testing.T) {
	port := startEngine(t)
	cases := []struct {
		method, path, header, value string
	}{
		{"GET", "/echo", "Origin", "https://example.com"},
		{"GET", "/echo", "Origin", "null"},
		{"GET", "/echo", "Sec-Fetch-Site", "cross-site"},
		{"GET", "/echo", "Sec-Fetch-Site", "same-origin"},
		{"POST", "/settings", "Origin", "http://127.0.0.1"},
		{"POST", "/torrents", "Sec-Fetch-Site", "none"},
		{"GET", "/stream?link=x", "Origin", "https://example.com"},
	}
	for _, tc := range cases {
		req, _ := http.NewRequest(tc.method, url(port, tc.path), strings.NewReader(`{"action":"list"}`))
		req.Header.Set(tc.header, tc.value)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s with %s: %s got %d, want 403", tc.method, tc.path, tc.header, tc.value, resp.StatusCode)
		}
	}
}

var settingsKinds = map[string]string{
	"CacheSize":                "number",
	"ReaderReadAHead":          "number",
	"PreloadCache":             "number",
	"UseDisk":                  "bool",
	"TorrentsSavePath":         "string",
	"RemoveCacheOnDrop":        "bool",
	"ForceEncrypt":             "bool",
	"RetrackersMode":           "number",
	"TorrentDisconnectTimeout": "number",
	"EnableDebug":              "bool",
	"EnableDLNA":               "bool",
	"FriendlyName":             "string",
	"EnableIPv6":               "bool",
	"DisableTCP":               "bool",
	"DisableUTP":               "bool",
	"DisableUPNP":              "bool",
	"DisableDHT":               "bool",
	"DisablePEX":               "bool",
	"DisableUpload":            "bool",
	"DownloadRateLimit":        "number",
	"UploadRateLimit":          "number",
	"ConnectionsLimit":         "number",
	"PeersListenPort":          "number",
	"EnableLPD":                "bool",
	"DefaultTrackers":          "string",
}

func kind(v any) string {
	switch v.(type) {
	case float64:
		return "number"
	case bool:
		return "bool"
	case string:
		return "string"
	default:
		return fmt.Sprintf("%T", v)
	}
}

func TestSettingsPayloadShape(t *testing.T) {
	port := startEngine(t)
	sets := getSettings(t, port)
	for key, want := range settingsKinds {
		v, ok := sets[key]
		if !ok {
			t.Errorf("settings missing %s", key)
			continue
		}
		if got := kind(v); got != want {
			t.Errorf("settings %s is %s, want %s", key, got, want)
		}
	}
}

func TestSettingsSetMergedRoundTripsAndDefResets(t *testing.T) {
	port := startEngine(t)
	sets := getSettings(t, port)
	def := map[string]any{}
	for k, v := range sets {
		def[k] = v
	}
	peer := freePort(t)
	want := map[string]any{
		"CacheSize":                float64(96 * 1024 * 1024),
		"ReaderReadAHead":          float64(80),
		"PreloadCache":             float64(25),
		"ConnectionsLimit":         float64(40),
		"TorrentDisconnectTimeout": float64(45),
		"DownloadRateLimit":        float64(1000),
		"UploadRateLimit":          float64(200),
		"PeersListenPort":          float64(peer),
		"RetrackersMode":           float64(0),
		"DisableDHT":               true,
		"DisableUPNP":              true,
		"DisablePEX":               true,
		"DisableUpload":            true,
		"EnableLPD":                false,
		"ForceEncrypt":             true,
		"FriendlyName":             "servetorr-test",
	}
	for k, v := range want {
		sets[k] = v
	}
	begin := time.Now()
	postOK(t, port, "/settings", map[string]any{"action": "set", "sets": sets})
	t.Logf("set took %s", time.Since(begin).Round(time.Millisecond))
	got := getSettings(t, port)
	for k, v := range want {
		if got[k] != v {
			t.Errorf("after set %s = %v, want %v", k, got[k], v)
		}
	}
	for k, v := range def {
		if _, changed := want[k]; changed {
			continue
		}
		a, _ := json.Marshal(v)
		b, _ := json.Marshal(got[k])
		if !bytes.Equal(a, b) {
			t.Errorf("after set %s changed from %s to %s", k, a, b)
		}
	}
	if got := echo(t, port); got != "ServeTorr torrserver "+tag {
		t.Fatalf("echo after set %q", got)
	}
	postOK(t, port, "/settings", map[string]any{"action": "def"})
	reset := getSettings(t, port)
	for _, k := range []string{"CacheSize", "ReaderReadAHead", "PreloadCache", "ConnectionsLimit", "TorrentDisconnectTimeout", "PeersListenPort", "DisableDHT"} {
		if reset[k] != def[k] {
			t.Errorf("after def %s = %v, want %v", k, reset[k], def[k])
		}
	}
}

func TestTorrentsLifecycle(t *testing.T) {
	port := startEngine(t)

	magnetHash := randomHash(t)
	begin := time.Now()
	data := postOK(t, port, "/torrents", map[string]any{
		"action":     "add",
		"link":       "magnet:?xt=urn:btih:" + magnetHash + "&dn=offline",
		"save_to_db": false,
	})
	if d := time.Since(begin); d > 5*time.Second {
		t.Errorf("add of a magnet without metadata took %s, want it to return immediately", d)
	}
	st := decodeStatus(t, data)
	if st.Hash != magnetHash {
		t.Errorf("add returned hash %q, want %q", st.Hash, magnetHash)
	}
	if st.Stat == nil || st.StatString == "" {
		t.Errorf("add status lacks stat/stat_string: %s", data)
	}
	if len(st.FileStats) != 0 {
		t.Errorf("magnet without metadata has file_stats: %s", data)
	}
	st = decodeStatus(t, postOK(t, port, "/torrents", map[string]any{"action": "get", "hash": magnetHash}))
	if st.Hash != magnetHash {
		t.Errorf("get returned hash %q", st.Hash)
	}

	files := []testFile{
		{[]string{"Season 1", "Show S01E10.mkv"}, 40000},
		{[]string{"Season 1", "Show S01E2.mkv"}, 30000},
		{[]string{"Season 1", "Show S01E1.mkv"}, 20000},
		{[]string{"sample.txt"}, 12},
	}
	torrentPath, fileHash := makeTorrent(t, "Show", files)
	data = postOK(t, port, "/torrents", map[string]any{"action": "add", "link": "file://" + torrentPath, "save_to_db": false})
	if st := decodeStatus(t, data); st.Hash != fileHash {
		t.Fatalf("add file returned hash %q, want %q", st.Hash, fileHash)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		st = decodeStatus(t, postOK(t, port, "/torrents", map[string]any{"action": "get", "hash": fileHash}))
		if len(st.FileStats) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	wantPaths := []string{
		"Show/Season 1/Show S01E1.mkv",
		"Show/Season 1/Show S01E2.mkv",
		"Show/Season 1/Show S01E10.mkv",
		"Show/sample.txt",
	}
	wantLengths := map[string]int64{
		"Show/Season 1/Show S01E1.mkv":  20000,
		"Show/Season 1/Show S01E2.mkv":  30000,
		"Show/Season 1/Show S01E10.mkv": 40000,
		"Show/sample.txt":               12,
	}
	if len(st.FileStats) != len(wantPaths) {
		t.Fatalf("file_stats %+v, want %d entries", st.FileStats, len(wantPaths))
	}
	for i, f := range st.FileStats {
		if f.ID != i+1 {
			t.Errorf("file_stats[%d].id = %d, want %d (1-based, consecutive)", i, f.ID, i+1)
		}
		if f.Path != wantPaths[i] {
			t.Errorf("file_stats[%d].path = %q, want %q (natural path order)", i, f.Path, wantPaths[i])
		}
		if f.Length != wantLengths[f.Path] {
			t.Errorf("file_stats[%d] %q length %d, want %d", i, f.Path, f.Length, wantLengths[f.Path])
		}
	}

	streamCases := []struct {
		index int
		code  int
		size  int64
	}{
		{1, http.StatusOK, 20000},
		{3, http.StatusOK, 40000},
		{99, http.StatusNotFound, 0},
	}
	for _, tc := range streamCases {
		link := "magnet:?xt=urn:btih:" + fileHash
		req, _ := http.NewRequest("HEAD", url(port, "/stream/x?link="+link+"&index="+strconv.Itoa(tc.index)+"&play"), nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("HEAD stream index %d: %v", tc.index, err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.code {
			t.Errorf("HEAD stream index %d: status %d, want %d", tc.index, resp.StatusCode, tc.code)
		}
		if tc.code == http.StatusOK && resp.ContentLength != tc.size {
			t.Errorf("HEAD stream index %d: length %d, want %d", tc.index, resp.ContentLength, tc.size)
		}
	}

	hashes := listHashes(t, port)
	if !contains(hashes, magnetHash) || !contains(hashes, fileHash) {
		t.Fatalf("list %v lacks %s or %s", hashes, magnetHash, fileHash)
	}

	postOK(t, port, "/torrents", map[string]any{"action": "drop", "hash": magnetHash})
	if contains(listHashes(t, port), magnetHash) {
		t.Errorf("drop left %s in list", magnetHash)
	}
	if code, _ := postRaw(t, port, "/torrents", map[string]any{"action": "get", "hash": magnetHash}); code != http.StatusNotFound {
		t.Errorf("get after drop: status %d, want 404", code)
	}

	postOK(t, port, "/torrents", map[string]any{"action": "rem", "hash": fileHash})
	if contains(listHashes(t, port), fileHash) {
		t.Errorf("rem left %s in list", fileHash)
	}

	for i := 0; i < 3; i++ {
		postOK(t, port, "/torrents", map[string]any{"action": "add", "link": "magnet:?xt=urn:btih:" + randomHash(t), "save_to_db": false})
	}
	if n := len(listHashes(t, port)); n != 3 {
		t.Errorf("list has %d torrents before wipe, want 3", n)
	}
	postOK(t, port, "/torrents", map[string]any{"action": "wipe"})
	if hashes := listHashes(t, port); len(hashes) != 0 {
		t.Errorf("wipe left %v", hashes)
	}
	if code, _ := postRaw(t, port, "/torrents", map[string]any{"action": "get", "hash": randomHash(t)}); code != http.StatusNotFound {
		t.Errorf("get of an unknown hash: status %d, want 404", code)
	}
}

func TestViewedListAndRemove(t *testing.T) {
	port := startEngine(t)
	data := postOK(t, port, "/viewed", map[string]any{"action": "list"})
	var list []map[string]any
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("viewed list %s: %v", data, err)
	}
	postOK(t, port, "/viewed", map[string]any{"action": "rem", "hash": randomHash(t), "file_index": -1})
}

func TestStopThenStartInOneProcess(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		port := start(t, dir)
		if got := echo(t, port); got != "ServeTorr torrserver "+tag {
			t.Fatalf("round %d echo %q", i, got)
		}
		getSettings(t, port)
		postOK(t, port, "/torrents", map[string]any{"action": "list"})
		waited := make(chan struct{})
		go func() {
			WaitTorrentServer()
			close(waited)
		}()
		StopTorrentServer()
		select {
		case <-waited:
		case <-time.After(10 * time.Second):
			t.Fatalf("round %d WaitTorrentServer did not return after stop", i)
		}
		if _, err := client.Get(url(port, "/echo")); err == nil {
			t.Fatalf("round %d engine still answers on %d after stop", i, port)
		}
	}
}

func TestStopCancelsReconnectLoop(t *testing.T) {
	dir := t.TempDir()
	port := start(t, dir)
	t.Cleanup(StopTorrentServer)
	peer := freePort(t)
	h, err := grab(peer)
	if err != nil {
		t.Fatal(err)
	}
	setPeerPort(t, port, peer)
	time.Sleep(4 * time.Second)
	StopTorrentServer()
	h.release()
	time.Sleep(5 * time.Second)
	h2, err := grab(peer)
	if err != nil {
		t.Fatalf("peer port %d taken after stop: %v", peer, err)
	}
	h2.release()
}

func TestStopStartDuringPendingSet(t *testing.T) {
	dir := t.TempDir()
	port := start(t, dir)
	t.Cleanup(StopTorrentServer)
	peer := freePort(t)
	setPeerPort(t, port, peer)
	time.Sleep(300 * time.Millisecond)
	StopTorrentServer()
	port = start(t, dir)
	time.Sleep(4 * time.Second)
	if h, err := grab(peer); err == nil {
		h.release()
		t.Fatalf("restarted engine does not hold peer port %d", peer)
	}
	if got := echo(t, port); got != "ServeTorr torrserver "+tag {
		t.Fatalf("echo after restart %q", got)
	}
	StopTorrentServer()
	time.Sleep(2 * time.Second)
	h, err := grab(peer)
	if err != nil {
		t.Fatalf("peer port %d taken after final stop: %v", peer, err)
	}
	h.release()
}

// Command loadtest drives a running gobb with a mix of requests for a set time,
// then checks that the server refused quickly and recovered. CI runs it against
// the image under the 256 MiB memory cap (section 10 of the design spec, "Load
// protection").
//
//	go run ./scripts/loadtest [flags] <base-url>
//	go run ./scripts/loadtest -duration 60s -c 64 -member-name alice \
//	    -member-password example-password -search-forbidden http://127.0.0.1:8080/
//
// The mix has these classes of request:
//   - page: a guest view of the index, a forum or a topic (the page cache);
//   - miss: a guest view of a page that does not exist: a topic past the last
//     one, or a page number past the end. These answer 404, and the cache does
//     not keep them, so each one renders;
//   - search: a guest search;
//   - signin: a wrong password, over many names and many IPv6 /64s, sent in
//     X-Forwarded-For as scripts/login-burst.sh does;
//   - member and msearch: page views and searches of a signed-in member, when
//     -member-name or -member-session gives a session.
//
// Each guest request carries an address from the documentation ranges in
// X-Forwarded-For. The server reads it only when it trusts the peer as a proxy
// (GOBB_TRUSTED_PROXIES), as .github/compose.ci.yaml does.
//
// Before the load, the command reads the index and the first page of each
// forum, as a guest and as the member, and keeps the pages that answer 200.
//
// After the load and a pause (-pause), it views each of those pages once more,
// one at a time, and /healthz. This is the recovery check.
//
// The command exits 1 when any of these holds:
//   - an answer is not in the allowed set of its class: 200 and 304, 404 for a
//     miss only, 403 for a guest search only with -search-forbidden, and 429 and
//     503 for every class;
//   - a 429 or a 503 took longer than -refuse-bound;
//   - a request failed with no answer: a refused or dropped connection, or no
//     answer within -timeout;
//   - a recovery view did not answer 200 within -recovery-bound.
//
// It exits 2 on a usage error, or when the set-up fails (no page found, no csrf
// field, a member sign-in that does not answer 303).
//
// The target must be a loopback address. -any-host lifts that check. Never aim
// this command at a server that you do not run.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The classes of request, in the order of the report.
const (
	classPage    = "page"
	classMiss    = "miss"
	classSearch  = "search"
	classSignin  = "signin"
	classMember  = "member"
	classMSearch = "msearch"
)

var classOrder = []string{classPage, classMiss, classSearch, classSignin, classMember, classMSearch}

// defaultMix is the weight of each class in the mix.
const defaultMix = "page=45,miss=15,search=10,signin=15,member=10,msearch=5"

// searchWords are the queries of the searches.
var searchWords = []string{"example", "welcome", "color", "board", "the", "kind", "quiet", "zzqx"}

// config holds the flags.
type config struct {
	base            *url.URL
	duration        time.Duration
	concurrency     int
	timeout         time.Duration
	refuseBound     time.Duration
	pause           time.Duration
	recoveryBound   time.Duration
	mix             map[string]int
	names           int
	addresses       int
	memberName      string
	memberPassword  string
	memberSession   string
	searchForbidden bool
	seed            uint64
	anyHost         bool
}

func main() {
	cfg, err := parseFlags(os.Args[1:], os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "loadtest:", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(run(ctx, cfg, os.Stdout))
}

// parseFlags reads the flags and the base URL.
func parseFlags(args []string, errOut io.Writer) (config, error) {
	fs := flag.NewFlagSet("loadtest", flag.ContinueOnError)
	fs.SetOutput(errOut)
	var cfg config
	var mix string
	fs.DurationVar(&cfg.duration, "duration", 60*time.Second, "how long the load runs")
	fs.IntVar(&cfg.concurrency, "c", 32, "requests at once: the number of workers")
	fs.DurationVar(&cfg.timeout, "timeout", 30*time.Second, "the longest wait for one answer; a longer wait is a failure")
	fs.DurationVar(&cfg.refuseBound, "refuse-bound", 2*time.Second, "the longest time that a 429 or a 503 may take")
	fs.DurationVar(&cfg.pause, "pause", 5*time.Second, "the pause between the end of the load and the recovery check")
	fs.DurationVar(&cfg.recoveryBound, "recovery-bound", 2*time.Second, "the longest time that a recovery view may take")
	fs.StringVar(&mix, "mix", defaultMix, "the weight of each class: page, miss, search, signin, member, msearch")
	fs.IntVar(&cfg.names, "names", 500, "the account names that the wrong passwords take turns over (load1 to loadN)")
	fs.IntVar(&cfg.addresses, "addresses", 5000, "the IPv6 /64s that the wrong passwords come from")
	fs.StringVar(&cfg.memberName, "member-name", "", "sign in as this member for the member classes")
	fs.StringVar(&cfg.memberPassword, "member-password", "", "the password of -member-name")
	fs.StringVar(&cfg.memberSession, "member-session", "", "the value of a gobb_session cookie, in place of -member-name")
	fs.BoolVar(&cfg.searchForbidden, "search-forbidden", false, "guests lack the search right on this board, so a guest search may answer 403")
	fs.Uint64Var(&cfg.seed, "seed", 0, "the seed of the random choices; 0 takes the time")
	fs.BoolVar(&cfg.anyHost, "any-host", false, "allow a target that is not a loopback address")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: loadtest [flags] <base-url>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return config{}, errors.New("want one base URL")
	}
	u, err := url.Parse(fs.Arg(0))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return config{}, fmt.Errorf("the base URL %q is not an http or https URL", fs.Arg(0))
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	u.RawQuery, u.Fragment = "", ""
	cfg.base = u
	if !cfg.anyHost && !isLoopback(u.Hostname()) {
		return config{}, fmt.Errorf("the host %q is not a loopback address; pass -any-host to aim at it", u.Hostname())
	}
	if cfg.mix, err = parseMix(mix); err != nil {
		return config{}, err
	}
	switch {
	case cfg.duration <= 0, cfg.timeout <= 0, cfg.refuseBound <= 0, cfg.recoveryBound <= 0, cfg.pause < 0:
		return config{}, errors.New("every time must be above zero (-pause may be zero)")
	case cfg.concurrency < 1:
		return config{}, errors.New("-c must be at least 1")
	case cfg.names < 1, cfg.addresses < 1, cfg.addresses > 1<<32:
		return config{}, errors.New("-names must be at least 1, and -addresses from 1 to 2^32")
	case cfg.memberName != "" && cfg.memberSession != "":
		return config{}, errors.New("give -member-name or -member-session, not both")
	case cfg.memberName != "" && cfg.memberPassword == "":
		return config{}, errors.New("-member-name needs -member-password")
	}
	if cfg.seed == 0 {
		cfg.seed = uint64(time.Now().UnixNano())
	}
	return cfg, nil
}

// isLoopback reports whether host is localhost or a loopback address.
func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// parseMix reads "class=weight,...". Every class must be known, every weight
// a whole number, and the sum above zero.
func parseMix(s string) (map[string]int, error) {
	m := map[string]int{}
	sum := 0
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, val, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("-mix: %q is not class=weight", part)
		}
		if !slices.Contains(classOrder, name) {
			return nil, fmt.Errorf("-mix: unknown class %q (want one of %s)", name, strings.Join(classOrder, ", "))
		}
		if _, dup := m[name]; dup {
			return nil, fmt.Errorf("-mix: class %q appears twice", name)
		}
		w, err := strconv.Atoi(val)
		if err != nil || w < 0 {
			return nil, fmt.Errorf("-mix: the weight of %q is not a whole number", name)
		}
		m[name] = w
		sum += w
	}
	if sum == 0 {
		return nil, errors.New("-mix: the weights add up to zero")
	}
	return m, nil
}

// allowed returns the statuses that class may answer.
func allowed(class string, searchForbidden bool) []int {
	set := []int{http.StatusOK, http.StatusNotModified, http.StatusTooManyRequests, http.StatusServiceUnavailable}
	switch class {
	case classMiss:
		set = append(set, http.StatusNotFound)
	case classSearch:
		if searchForbidden {
			set = append(set, http.StatusForbidden)
		}
	}
	return set
}

// isRefusal reports whether code is an answer of the load protection.
func isRefusal(code int) bool {
	return code == http.StatusTooManyRequests || code == http.StatusServiceUnavailable
}

// tester holds what the workers share. Nothing in it changes during the load.
type tester struct {
	cfg    config
	client *http.Client
	// guest and member are the paths (relative to base) that answered 200 at
	// set-up, for each viewer.
	guest, member []string
	// forums and topics are the ids found as a guest, for the misses.
	forums, topics []int
	maxTopic       int
	// loginCookie and csrf serve every wrong password: one guest cookie.
	loginCookie []*http.Cookie
	csrf        string
	// session is the member's gobb_session cookie, or nil.
	session *http.Cookie
	// classes are the classes in the mix, and weights their running sums.
	classes []string
	weights []int
}

// failure is one request that broke a rule.
type failure struct {
	kind   string // "status", "slow", "error"
	class  string
	path   string
	status int
	took   time.Duration
	err    string
}

func (f failure) String() string {
	switch f.kind {
	case "status":
		return fmt.Sprintf("%s %s: status %d is not allowed (took %s)", f.class, f.path, f.status, ms(f.took))
	case "slow":
		return fmt.Sprintf("%s %s: status %d took %s", f.class, f.path, f.status, ms(f.took))
	}
	return fmt.Sprintf("%s %s: %s (after %s)", f.class, f.path, f.err, ms(f.took))
}

// classStats are the results of one class.
type classStats struct {
	statuses map[int]int
	errors   int
	took     []time.Duration
	slowest  time.Duration
	slowPath string
	slowCode int
}

func (c *classStats) add(path string, code int, took time.Duration) {
	if c.statuses == nil {
		c.statuses = map[int]int{}
	}
	if code == 0 {
		c.errors++
	} else {
		c.statuses[code]++
	}
	c.took = append(c.took, took)
	if took >= c.slowest {
		c.slowest, c.slowPath, c.slowCode = took, path, code
	}
}

func (c *classStats) merge(o *classStats) {
	if c.statuses == nil {
		c.statuses = map[int]int{}
	}
	for k, v := range o.statuses {
		c.statuses[k] += v
	}
	c.errors += o.errors
	c.took = append(c.took, o.took...)
	if o.slowest >= c.slowest && len(o.took) > 0 {
		c.slowest, c.slowPath, c.slowCode = o.slowest, o.slowPath, o.slowCode
	}
}

// result is what one worker, or the whole load, saw.
type result struct {
	classes  map[string]*classStats
	failures map[string]int // by kind
	examples []failure      // the first few of each kind
}

func newResult() *result {
	return &result{classes: map[string]*classStats{}, failures: map[string]int{}}
}

// maxExamples is the number of failures of each kind that the report shows.
const maxExamples = 5

func (r *result) fail(f failure) {
	r.failures[f.kind]++
	if r.failures[f.kind] <= maxExamples {
		r.examples = append(r.examples, f)
	}
}

func (r *result) stats(class string) *classStats {
	c := r.classes[class]
	if c == nil {
		c = &classStats{}
		r.classes[class] = c
	}
	return c
}

func (r *result) merge(o *result) {
	for k, v := range o.classes {
		r.stats(k).merge(v)
	}
	for k, v := range o.failures {
		before := r.failures[k]
		r.failures[k] += v
		for _, f := range o.examples {
			if f.kind == k && before < maxExamples {
				r.examples = append(r.examples, f)
				before++
			}
		}
	}
}

// run runs the set-up, the load and the recovery check, prints the report to
// out, and returns the exit status.
func run(ctx context.Context, cfg config, out io.Writer) int {
	return runWithHooks(ctx, cfg, out, hooks{})
}

// hooks let a test change the server between the phases.
type hooks struct {
	// started runs after the set-up, before the load; ended after the load.
	started, ended func()
}

func runWithHooks(ctx context.Context, cfg config, out io.Writer, h hooks) int {
	t, err := setup(ctx, cfg)
	if err != nil {
		fmt.Fprintln(out, "loadtest: set-up failed:", err)
		return 2
	}
	fmt.Fprintf(out, "loadtest: %s at concurrency %d against %s (seed %d)\n", cfg.duration, cfg.concurrency, cfg.base, cfg.seed)
	fmt.Fprintf(out, "targets: %d guest pages (%d forums, %d topics), %d member pages\n",
		len(t.guest), len(t.forums), len(t.topics), len(t.member))
	fmt.Fprintf(out, "mix: %s\n", t.mixString())

	if h.started != nil {
		h.started()
	}
	start := time.Now()
	res := t.load(ctx)
	elapsed := time.Since(start)
	if h.ended != nil {
		h.ended()
	}
	t.printLoad(out, res, elapsed)

	if ctx.Err() != nil {
		fmt.Fprintln(out, "FAIL: interrupted")
		return 1
	}
	fmt.Fprintf(out, "recovery: a pause of %s, then each page once, within %s\n", cfg.pause, cfg.recoveryBound)
	select {
	case <-time.After(cfg.pause):
	case <-ctx.Done():
		fmt.Fprintln(out, "FAIL: interrupted")
		return 1
	}
	bad := t.recovery(ctx, out)

	failed := false
	for _, kind := range []string{"status", "slow", "error"} {
		if n := res.failures[kind]; n > 0 {
			failed = true
			fmt.Fprintf(out, "FAIL: %d %s\n", n, failureWords[kind])
		}
	}
	for _, f := range res.examples {
		fmt.Fprintln(out, "  "+f.String())
	}
	if bad > 0 {
		failed = true
		fmt.Fprintf(out, "FAIL: %d recovery views did not answer 200 within %s\n", bad, cfg.recoveryBound)
	}
	if failed {
		return 1
	}
	fmt.Fprintln(out, "ok: every answer was allowed, every refusal was quick, and the server recovered")
	return 0
}

var failureWords = map[string]string{
	"status": "answers with a status that the class does not allow",
	"slow":   "refusals (429 or 503) slower than the bound",
	"error":  "requests with no answer (connection error or timeout)",
}

// setup builds the client, finds the pages, gets the csrf token of the
// sign-in form and signs the member in.
func setup(ctx context.Context, cfg config) (*tester, error) {
	tr := &http.Transport{
		Proxy:               nil,
		MaxIdleConns:        cfg.concurrency * 2,
		MaxIdleConnsPerHost: cfg.concurrency * 2,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true,
	}
	t := &tester{
		cfg: cfg,
		client: &http.Client{
			Transport: tr,
			Timeout:   cfg.timeout,
			// A redirect is an answer to check, not to follow.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	for _, c := range classOrder {
		w := cfg.mix[c]
		if w == 0 {
			continue
		}
		if (c == classMember || c == classMSearch) && cfg.memberName == "" && cfg.memberSession == "" {
			continue
		}
		sum := w
		if n := len(t.weights); n > 0 {
			sum += t.weights[n-1]
		}
		t.classes = append(t.classes, c)
		t.weights = append(t.weights, sum)
	}
	if len(t.classes) == 0 {
		return nil, errors.New("the mix has no class to run (the member classes need a member)")
	}

	var err error
	if t.guest, t.forums, t.topics, err = t.discover(ctx, nil); err != nil {
		return nil, fmt.Errorf("guest pages: %w", err)
	}
	if len(t.topics) == 0 {
		return nil, errors.New("a guest sees no topic: the board is empty, or not loaded")
	}
	t.maxTopic = slices.Max(t.topics)

	if err := t.loginForm(ctx); err != nil {
		return nil, err
	}
	switch {
	case cfg.memberSession != "":
		t.session = &http.Cookie{Name: "gobb_session", Value: cfg.memberSession}
	case cfg.memberName != "":
		if t.session, err = t.signIn(ctx); err != nil {
			return nil, err
		}
	}
	if t.session != nil {
		if t.member, _, _, err = t.discover(ctx, t.session); err != nil {
			return nil, fmt.Errorf("member pages: %w", err)
		}
	}
	return t, nil
}

// linkRE finds the forum and topic links of a page: href="<base>f/N" or t/N,
// with any query or fragment after the number.
func (t *tester) linkRE() *regexp.Regexp {
	return regexp.MustCompile(`href="` + regexp.QuoteMeta(t.cfg.base.Path) + `([ft])/([0-9]+)[?#"]`)
}

// maxTargets bounds the pages found at set-up for each viewer.
const maxTargets = 500

// discover reads the index and the first page of each forum, as a guest or
// with the session, and returns the paths that answered 200 (the index first),
// with the forum and topic ids.
func (t *tester) discover(ctx context.Context, session *http.Cookie) (paths []string, forums, topics []int, err error) {
	re := t.linkRE()
	body, code, err := t.fetch(ctx, "", session)
	if err != nil {
		return nil, nil, nil, err
	}
	if code != http.StatusOK {
		return nil, nil, nil, fmt.Errorf("the index answered %d", code)
	}
	paths = []string{""}
	seenF, seenT := map[int]bool{}, map[int]bool{}
	collect := func(body string) {
		for _, m := range re.FindAllStringSubmatch(body, -1) {
			id, err := strconv.Atoi(m[2])
			if err != nil {
				continue
			}
			if m[1] == "f" && !seenF[id] {
				seenF[id] = true
				forums = append(forums, id)
			}
			if m[1] == "t" && !seenT[id] {
				seenT[id] = true
				topics = append(topics, id)
			}
		}
	}
	collect(body)
	var okForums []int
	for _, id := range forums {
		if len(paths) >= maxTargets {
			break
		}
		p := "f/" + strconv.Itoa(id)
		body, code, err := t.fetch(ctx, p, session)
		if err != nil {
			return nil, nil, nil, err
		}
		if code == http.StatusOK {
			paths = append(paths, p)
			okForums = append(okForums, id)
			collect(body)
		}
	}
	var okTopics []int
	for _, id := range topics {
		if len(paths) >= maxTargets {
			break
		}
		p := "t/" + strconv.Itoa(id)
		_, code, err := t.fetch(ctx, p, session)
		if err != nil {
			return nil, nil, nil, err
		}
		if code == http.StatusOK {
			paths = append(paths, p)
			okTopics = append(okTopics, id)
		}
	}
	return paths, okForums, okTopics, nil
}

// fetch GETs base+path and returns the body and status.
func (t *tester) fetch(ctx context.Context, path string, session *http.Cookie) (string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.cfg.base.String()+path, nil)
	if err != nil {
		return "", 0, err
	}
	if session != nil {
		req.AddCookie(session)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", 0, err
	}
	return string(b), resp.StatusCode, nil
}

var csrfRE = regexp.MustCompile(`name="csrf" value="([^"]*)"`)

// loginForm reads the sign-in form once, as a guest, for the cookie and the
// csrf token that every wrong password and the member sign-in carry.
func (t *tester) loginForm(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.cfg.base.String()+"login", nil)
	if err != nil {
		return err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("GET /login: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("GET /login: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET /login answered %d", resp.StatusCode)
	}
	m := csrfRE.FindSubmatch(b)
	if m == nil || len(m[1]) == 0 {
		return errors.New("GET /login: no csrf field in the form")
	}
	t.csrf = string(m[1])
	t.loginCookie = resp.Cookies()
	return nil
}

// signIn signs the member in and returns the session cookie.
func (t *tester) signIn(ctx context.Context) (*http.Cookie, error) {
	form := url.Values{"csrf": {t.csrf}, "name": {t.cfg.memberName}, "password": {t.cfg.memberPassword}, "next": {"/"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.cfg.base.String()+"login", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range t.loginCookie {
		req.AddCookie(c)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("member sign-in: %w", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		return nil, fmt.Errorf("member sign-in answered %d, want 303", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "gobb_session" && c.Value != "" {
			return &http.Cookie{Name: c.Name, Value: c.Value}, nil
		}
	}
	return nil, errors.New("member sign-in set no gobb_session cookie")
}

// load runs the workers until the duration ends and returns what they saw.
func (t *tester) load(ctx context.Context) *result {
	ctx, cancel := context.WithTimeout(ctx, t.cfg.duration)
	defer cancel()
	results := make([]*result, t.cfg.concurrency)
	var wg sync.WaitGroup
	for i := range t.cfg.concurrency {
		res := newResult()
		results[i] = res
		rng := rand.New(rand.NewPCG(t.cfg.seed, uint64(i)))
		wg.Go(func() {
			for ctx.Err() == nil {
				t.one(ctx, rng, res)
			}
		})
	}
	wg.Wait()
	all := newResult()
	for _, r := range results {
		all.merge(r)
	}
	return all
}

// pick returns a class of the mix by weight.
func (t *tester) pick(rng *rand.Rand) string {
	n := rng.IntN(t.weights[len(t.weights)-1])
	i := sort.SearchInts(t.weights, n+1)
	return t.classes[i]
}

// one sends one request of a random class and records it in res. A request
// that the end of the load cut short records nothing.
func (t *tester) one(ctx context.Context, rng *rand.Rand, res *result) {
	class := t.pick(rng)
	method, path, body := http.MethodGet, "", ""
	var session *http.Cookie
	xff := guestAddress(rng)
	switch class {
	case classPage:
		path = t.guest[rng.IntN(len(t.guest))]
	case classMiss:
		path = t.missPath(rng)
	case classSearch:
		path = "search?q=" + searchWords[rng.IntN(len(searchWords))]
	case classSignin:
		method, path = http.MethodPost, "login"
		n := rng.IntN(t.cfg.names) + 1
		a := rng.IntN(t.cfg.addresses)
		xff = fmt.Sprintf("2001:db8:%x:%x::1", a>>16, a&0xffff)
		body = url.Values{
			"csrf": {t.csrf}, "name": {"load" + strconv.Itoa(n)},
			"password": {"wrong load password " + strconv.Itoa(rng.IntN(1<<30))}, "next": {"/"},
		}.Encode()
	case classMember:
		path, session, xff = t.member[rng.IntN(len(t.member))], t.session, ""
	case classMSearch:
		path, session, xff = "search?q="+searchWords[rng.IntN(len(searchWords))], t.session, ""
	}
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.cfg.base.String()+path, rd)
	if err != nil {
		res.fail(failure{kind: "error", class: class, path: "/" + path, err: err.Error()})
		return
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for _, c := range t.loginCookie {
			req.AddCookie(c)
		}
	}
	if session != nil {
		req.AddCookie(session)
	}
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	code, took, err := t.send(req)
	if err != nil && ctx.Err() != nil {
		// The load ended during this request; it says nothing of the server.
		return
	}
	t.check(res, class, "/"+path, code, took, err)
}

// check records one answer in res, with any rule that it broke.
func (t *tester) check(res *result, class, path string, code int, took time.Duration, err error) {
	res.stats(class).add(path, code, took)
	switch {
	case err != nil:
		res.fail(failure{kind: "error", class: class, path: path, took: took, err: err.Error()})
	case !slices.Contains(allowed(class, t.cfg.searchForbidden), code):
		res.fail(failure{kind: "status", class: class, path: path, status: code, took: took})
	case isRefusal(code) && took > t.cfg.refuseBound:
		res.fail(failure{kind: "slow", class: class, path: path, status: code, took: took})
	}
}

// send sends req, reads the whole body, and returns the status and the time
// to the end of the body. code is 0 when err is set.
func (t *tester) send(req *http.Request) (int, time.Duration, error) {
	start := time.Now()
	resp, err := t.client.Do(req)
	if err != nil {
		return 0, time.Since(start), err
	}
	_, err = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	took := time.Since(start)
	if err != nil {
		return 0, took, err
	}
	return resp.StatusCode, took, nil
}

// missPath returns a guest page that does not exist: a topic past the last
// one, or a page number past the end of a topic or a forum.
func (t *tester) missPath(rng *rand.Rand) string {
	far := strconv.Itoa(1000 + rng.IntN(100_000))
	n := rng.IntN(3)
	if n == 2 && len(t.forums) == 0 {
		n = 0
	}
	switch n {
	case 0:
		return "t/" + strconv.Itoa(t.maxTopic+1+rng.IntN(1_000_000))
	case 1:
		return "t/" + strconv.Itoa(t.topics[rng.IntN(len(t.topics))]) + "?page=" + far
	default:
		return "f/" + strconv.Itoa(t.forums[rng.IntN(len(t.forums))]) + "?page=" + far
	}
}

// guestAddress returns an IPv4 address from the documentation ranges
// (RFC 5737): 768 guests.
func guestAddress(rng *rand.Rand) string {
	nets := []string{"192.0.2.", "198.51.100.", "203.0.113."}
	return nets[rng.IntN(len(nets))] + strconv.Itoa(rng.IntN(256))
}

// recovery views each page that answered 200 at set-up, and /healthz, one at a
// time. It prints a line for each viewer and returns the number of views that
// did not answer 200 within the bound.
func (t *tester) recovery(ctx context.Context, out io.Writer) int {
	bad := 0
	view := func(who, path string, session *http.Cookie) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.cfg.base.String()+path, nil)
		if err != nil {
			bad++
			fmt.Fprintf(out, "  recovery %s /%s: %v\n", who, path, err)
			return
		}
		if session != nil {
			req.AddCookie(session)
		}
		code, took, err := t.send(req)
		switch {
		case err != nil:
			bad++
			fmt.Fprintf(out, "  recovery %s /%s: %v (after %s)\n", who, path, err, ms(took))
		case code != http.StatusOK:
			bad++
			fmt.Fprintf(out, "  recovery %s /%s: status %d, want 200 (took %s)\n", who, path, code, ms(took))
		case took > t.cfg.recoveryBound:
			bad++
			fmt.Fprintf(out, "  recovery %s /%s: took %s\n", who, path, ms(took))
		}
	}
	for _, p := range t.guest {
		view("guest", p, nil)
	}
	for _, p := range t.member {
		view("member", p, t.session)
	}
	view("guest", "healthz", nil)
	fmt.Fprintf(out, "recovery: %d guest and %d member views and /healthz, %d failed\n", len(t.guest), len(t.member), bad)
	return bad
}

// mixString prints the classes that run, with their weights.
func (t *tester) mixString() string {
	var parts []string
	prev := 0
	for i, c := range t.classes {
		parts = append(parts, fmt.Sprintf("%s=%d", c, t.weights[i]-prev))
		prev = t.weights[i]
	}
	return strings.Join(parts, ",")
}

// printLoad prints one line for each class, and the totals.
func (t *tester) printLoad(out io.Writer, res *result, elapsed time.Duration) {
	fmt.Fprintf(out, "%-8s %8s %7s  %-6s %-6s %-6s %-8s  %s\n", "class", "requests", "per s", "p50", "p95", "p99", "max", "statuses; the slowest")
	total := 0
	refusals := map[int]int{}
	for _, c := range classOrder {
		s := res.classes[c]
		if s == nil || len(s.took) == 0 {
			continue
		}
		n := len(s.took)
		total += n
		slices.Sort(s.took)
		var codes []int
		for k := range s.statuses {
			codes = append(codes, k)
			if isRefusal(k) {
				refusals[k] += s.statuses[k]
			}
		}
		slices.Sort(codes)
		var st []string
		for _, k := range codes {
			st = append(st, fmt.Sprintf("%d x%d", k, s.statuses[k]))
		}
		if s.errors > 0 {
			st = append(st, fmt.Sprintf("error x%d", s.errors))
		}
		slow := strconv.Itoa(s.slowCode)
		if s.slowCode == 0 {
			slow = "error"
		}
		fmt.Fprintf(out, "%-8s %8d %7.1f  %-6s %-6s %-6s %-8s  %s; %s %s\n", c, n, float64(n)/elapsed.Seconds(),
			ms(percentile(s.took, 50)), ms(percentile(s.took, 95)), ms(percentile(s.took, 99)), ms(s.slowest),
			strings.Join(st, ", "), slow, s.slowPath)
	}
	fmt.Fprintf(out, "total: %d requests in %s (%.1f per s); 429 x%d, 503 x%d\n", total, elapsed.Round(time.Millisecond),
		float64(total)/elapsed.Seconds(), refusals[http.StatusTooManyRequests], refusals[http.StatusServiceUnavailable])
}

// percentile returns the p-th percentile (nearest rank) of sorted.
func percentile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := (p*len(sorted) + 99) / 100
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}

// ms prints d in milliseconds, with one decimal under 10 ms.
func ms(d time.Duration) string {
	f := float64(d) / float64(time.Millisecond)
	if f < 10 {
		return strconv.FormatFloat(f, 'f', 1, 64) + "ms"
	}
	return strconv.FormatFloat(f, 'f', 0, 64) + "ms"
}

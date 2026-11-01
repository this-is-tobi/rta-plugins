package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// docker.overview: whether the daemon is well, what is not, and where the disk
// went.
//
// The composed capability an operator actually puts on a dashboard. Its compact
// form is what a tile and an agent read, so it opens with the verdict, in a word
// the theme colours, and runs from the state of the containers to the disk, with
// nothing that never changes. The daemon answering is the verdict and not a row
// of its own: a daemon that does not is the whole of the page, and one that does
// is the "ok".
//
// Two calls make the compact page, `ps` and `system df`, one after the other: a
// daemon that does not answer the first has no second to wait for. The image
// list, which only the detailed page reads, is a third that it makes and the
// compact page does not. It used to be the second call, summed into a figure
// that counted every layer shared between images once for each of them and left
// out the volumes and the build cache, which are where the disk usually went.

// overviewRefresh is how often a dashboard tile of docker.overview is re-run, for
// the person who names it in their config. `system df` adds up the size of every
// volume, which the daemon does by walking them and which takes seconds on a host
// with many, and nothing it reports moves that fast: a container that fails its
// healthcheck is a minute old before the daemon has run the check three times.
const overviewRefresh = 2 * time.Minute

// rowRoom is the width a value of the compact answer is held to: the 80 columns
// of a line less the widest key (`containers`) and the two cells a renderer sets
// beside it. The page has four rows and a tile eight lines, so a value that
// wraps on a narrow tile costs nothing that is on it.
const rowRoom = 66

// unansweredRoom is the width the reason of a daemon that could not be read may
// take: the 80 columns of a line less the widest key of that page, `what to
// check`, and the two cells after it.
const unansweredRoom = 65

// worstContainers is how many containers the problems row names at most.
const worstContainers = 6

// Words of the verdict: the grade, and what the grade is of.
const (
	rankFail = iota
	rankUnread
	rankWarn
)

type clause struct {
	rank int
	text string
}

func runOverview(ctx context.Context, req plugin.Request) (view.View, error) {
	c, verr := connectionOf(req)
	if verr != nil {
		return nil, verr
	}
	rows, verr := fetchContainers(ctx, c, true)
	if verr != nil {
		return unanswered(verr, c), nil
	}
	usage, usageErr := fetchDiskUsage(ctx, c)
	p := readOverview(rows, usage, usageErr)
	if !req.Bool("detail") {
		return view.KeyValue{Pairs: p.compact()}, nil
	}
	images, imgErr := fetchImages(ctx, c)
	return p.detail(images, imgErr), nil
}

// unanswered is the page of a daemon that could not be read. It is the headline
// and not a failure: this is the capability somebody runs *because* something is
// wrong, so it says what happened and what to check. The reason is one line, and
// the hint is whole, because advice cut short at a column is not advice.
func unanswered(e *view.Error, c connection) view.View {
	answer := daemonAnswer(e.Code)
	return view.KeyValue{Pairs: []view.Pair{
		{Key: "status", Value: "fail — the daemon " + answer},
		{Key: "daemon", Value: format.Clip(oneLine(answer+" — "+e.Message), unansweredRoom)},
		{Key: "what to check", Value: hintOf(e, c)},
	}}
}

// overviewPage is the daemon as the page reads it, read once and put into words
// twice, in the compact page and in the first section of the detailed one.
type overviewPage struct {
	rows     []containerRow
	running  int
	clean    int
	problems []problem
	clauses  []clause
	usage    []diskRow
	usageErr *view.Error
}

// problem is a container the page names.
type problem struct {
	row  containerRow
	kind kind
}

func readOverview(rows []containerRow, usage []diskRow, usageErr *view.Error) overviewPage {
	p := overviewPage{rows: rows, usage: usage, usageErr: usageErr}
	counts := map[kind]int{}
	for _, r := range rows {
		if strings.EqualFold(r.State, "running") {
			p.running++
		}
		k := kindOf(r)
		switch k {
		case kindFine:
		case kindExitedCleanly:
			p.clean++
		default:
			p.problems = append(p.problems, problem{r, k})
			counts[k]++
		}
	}
	// The broken ones first, then the ones that crashed, then the rest, each in
	// the order the rows came in (by name).
	sort.SliceStable(p.problems, func(i, j int) bool { return p.problems[i].kind.rank() < p.problems[j].kind.rank() })

	for _, c := range []struct {
		k    kind
		rank int
		what string
	}{
		{kindUnhealthy, rankFail, "unhealthy"},
		{kindRestarting, rankFail, "restarting"},
		{kindDead, rankFail, "dead"},
		{kindCrashed, rankWarn, "exited non-zero"},
		{kindStopped, rankWarn, "not running"},
	} {
		if n := counts[c.k]; n > 0 {
			p.clauses = append(p.clauses, clause{c.rank, fmt.Sprintf("%d %s", n, c.what)})
		}
	}
	if usageErr != nil {
		p.clauses = append(p.clauses, clause{rankUnread, "disk could not be read"})
	}
	return p
}

// verdict is the first row: ok, or the worst grade and the facts that earn it,
// as many as fit and the rest counted.
func (p overviewPage) verdict() string {
	if len(p.clauses) == 0 {
		return "ok"
	}
	all := append([]clause(nil), p.clauses...)
	sort.SliceStable(all, func(i, j int) bool { return all[i].rank < all[j].rank })
	word := "warn"
	if all[0].rank == rankFail {
		word = "fail"
	}
	texts := make([]string, len(all))
	for i, c := range all {
		texts[i] = c.text
	}
	lead := word + " — "
	return lead + fitList(texts, rowRoom-utf8.RuneCountInString(lead), len(texts), moreCount)
}

// containersRow is how many are running of those that exist, and the one-shots
// that finished are counted and not named: a compose project's migrations and
// seeds were six of the ten slots of a list that held the two containers that
// were broken in its sixth place.
func (p overviewPage) containersRow() string {
	v := fmt.Sprintf("%d running of %d", p.running, len(p.rows))
	if p.clean > 0 {
		v += fmt.Sprintf(" · %d exited cleanly", p.clean)
	}
	return v
}

// problemsRow names the containers that are not well, the broken ones first and
// then those that crashed with the code they exited with, six at most and as many
// as the line holds.
func (p overviewPage) problemsRow() string {
	if len(p.problems) == 0 {
		return "none"
	}
	names := make([]string, len(p.problems))
	for i, pr := range p.problems {
		names[i] = pr.row.Names + " (" + pr.kind.word(pr.row) + ")"
	}
	return fitList(names, rowRoom, worstContainers, moreWords)
}

func (p overviewPage) diskRow() string {
	if p.usageErr != nil {
		return unreadable(p.usageErr)
	}
	return diskValue(p.usage)
}

// compact is the page a tile and an agent read.
func (p overviewPage) compact() []view.Pair {
	return []view.Pair{
		{Key: "status", Value: p.verdict()},
		{Key: "containers", Value: p.containersRow()},
		{Key: "problems", Value: p.problemsRow()},
		{Key: "disk", Value: p.diskRow()},
	}
}

// unreadable is how a row says the daemon would not tell it what it is made of.
// **A dropped row reads as a compact report, not as a question nobody
// answered.** The images line simply vanished when the image query failed — a
// daemon that answers /containers/json and refuses /images/json is an ordinary
// socket-permission split — so a glance at the overview said nothing about
// images at all, which is also exactly what a host with no images would look
// like here. The reason is one line: the whole of it is on the detailed page's
// warnings.
func unreadable(e *view.Error) string {
	return format.Clip(view.CouldNotRead(oneLine(e.Message)), rowRoom)
}

// detail is the whole page: the rows of the compact one beside the daemon's own
// word that it answers and the images, then the tables of what they are made of.
func (p overviewPage) detail(images []imageRow, imgErr *view.Error) view.View {
	pairs := []view.Pair{
		{Key: "status", Value: p.verdict()},
		{Key: "daemon", Value: "answering"},
		{Key: "containers", Value: p.containersRow()},
		{Key: "problems", Value: p.problemsRow()},
		{Key: "disk", Value: p.diskRow()},
		{Key: "images", Value: imagesRow(images, imgErr)},
	}
	sections := []view.Section{{ID: "daemon", Title: "Daemon", View: view.KeyValue{Pairs: pairs}}}

	// Everything that is not running and well, the one-shots that finished
	// included: the compact page counts them, and this is where they are named.
	var unwell []containerRow
	for _, r := range p.rows {
		if kindOf(r) != kindFine {
			unwell = append(unwell, r)
		}
	}
	if len(unwell) > 0 {
		sections = append(sections, view.Section{
			ID: "containers", Title: "Containers not running or not healthy", View: containerTable(unwell)})
	} else {
		sections = append(sections, view.Section{
			ID: "containers", Title: "Containers",
			View: view.Text{Body: fmt.Sprintf("all %s running and healthy",
				count(len(p.rows), "container is", "containers are"))}})
	}
	if p.usageErr == nil {
		sections = append(sections, view.Section{ID: "disk", Title: "Disk usage", View: diskTable(p.usage)})
	}
	if imgErr == nil {
		top := images
		if len(top) > 10 {
			top = top[:10]
		}
		sections = append(sections, view.Section{
			ID: "images", Title: "Largest images", View: imageTable(top)})
	}
	page := view.Sections{Items: sections}
	// Sections carries what the page could not produce, for the reason
	// pkg/view states on the field itself: a degraded page that looks complete
	// is how a check comes back green.
	if p.usageErr != nil {
		page.Warnings = append(page.Warnings, *p.usageErr)
	}
	if imgErr != nil {
		page.Warnings = append(page.Warnings, *imgErr)
	}
	return page
}

// imagesRow is how many images there are and how many of them are untagged
// leftovers of a rebuild. Not a sum of their sizes: the size `image ls` prints is
// the image with every layer it shares, so the sum counts a base layer once for
// each image built on it, and the table of the largest images beside it has the
// sizes one by one.
func imagesRow(images []imageRow, imgErr *view.Error) string {
	if imgErr != nil {
		return unreadable(imgErr)
	}
	dangling := 0
	for _, im := range images {
		if im.dangling() {
			dangling++
		}
	}
	v := fmt.Sprintf("%d", len(images))
	if dangling > 0 {
		v += fmt.Sprintf(", %d dangling", dangling)
	}
	return v
}

// daemonAnswer is what the headline says happened, in the four ways
// kube.overview says the same four: the daemon was asked and said nothing, was
// asked and said no, was never asked, or something else, which claims nothing.
//
// "did not answer" was said of every one of them, and it sends somebody to
// start a daemon or check a network. For a docker that is not on the PATH, a
// context that does not exist or a socket that refused this account, the daemon
// is fine and the reader is sent the wrong way. The default is the vaguest of
// the four for the reason kube's is: a code nobody mapped, docker.failed
// above all, is as likely a daemon that answered as one that did not.
func daemonAnswer(code string) string {
	switch code {
	case "docker.unreachable":
		return "did not answer"
	case "docker.denied":
		return "answered, and refused"
	case "docker.cli.missing", "docker.context.unknown", "docker.cancelled":
		return "was never contacted"
	}
	return "could not be read"
}

func hintOf(e *view.Error, c connection) string {
	if e == nil || e.Hint == "" {
		return "`" + c.infoLine() + "` asks the same question directly"
	}
	return e.Hint
}

// How the items a list leaves out are counted: "a, b and 3 more" for a list
// nothing else counts, and "a, b +3" for the verdict.
const (
	moreWords = " and %d more"
	moreCount = " +%d"
)

// fitList is as many of items as fit in room, at most most of them, with the
// rest counted as more says. It names the first whatever the room.
func fitList(items []string, room, most int, more string) string {
	tail := func(left int) string {
		if left == 0 {
			return ""
		}
		return fmt.Sprintf(more, left)
	}
	for k := min(len(items), most); k >= 1; k-- {
		if s := strings.Join(items[:k], ", ") + tail(len(items)-k); utf8.RuneCountInString(s) <= room {
			return s
		}
	}
	return items[0] + tail(len(items)-1)
}

// oneLine is text on a single line, whatever the daemon wrote.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

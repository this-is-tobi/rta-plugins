package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/this-is-tobi/rta/pkg/format"
	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// docker.overview: what is running, what is not well, and where the disk went.
//
// The composed capability an operator actually puts on a dashboard. It leads
// with whether the daemon answers, because every other number here is
// meaningless if it does not.

func runOverview(ctx context.Context, req plugin.Request) (view.View, error) {
	c, verr := connectionOf(req)
	if verr != nil {
		return nil, verr
	}
	rows, verr := fetchContainers(ctx, c, true)
	if verr != nil {
		// The daemon not answering is the headline rather than a failure:
		// this is the capability somebody runs *because* something is wrong.
		return view.KeyValue{Pairs: []view.Pair{
			{Key: "daemon", Value: daemonAnswer(verr.Code) + " — " + verr.Message},
			{Key: "what to check", Value: hintOf(verr, c)},
		}}, nil
	}

	running, sick := 0, []containerRow{}
	for _, r := range rows {
		if strings.EqualFold(r.State, "running") {
			running++
		}
		if unhealthy(r) {
			sick = append(sick, r)
		}
	}
	pairs := []view.Pair{
		{Key: "daemon", Value: "answering"},
		{Key: "containers", Value: fmt.Sprintf("%d running of %d", running, len(rows))},
	}
	if len(sick) > 0 {
		names := make([]string, 0, len(sick))
		for _, r := range sick {
			names = append(names, r.Names+" ("+whatIsWrong(r)+")")
		}
		pairs = append(pairs, view.Pair{
			Key:   "not running or not healthy",
			Value: truncate(names, 6)})
	}

	images, imgErr := fetchImages(ctx, c)
	var dangling int
	if imgErr == nil {
		var total int64
		for _, im := range images {
			total += parseSize(im.Size)
			if im.dangling() {
				dangling++
			}
		}
		v := fmt.Sprintf("%d, %s", len(images), format.Bytes(total))
		if dangling > 0 {
			v += fmt.Sprintf(" — %d dangling", dangling)
		}
		pairs = append(pairs, view.Pair{Key: "images", Value: v})
	} else {
		// **A dropped row reads as a compact report, not as a question
		// nobody answered.** The images line simply vanished when the image
		// query failed — a daemon that answers /containers/json and refuses
		// /images/json is an ordinary socket-permission split — so a glance
		// at the overview said nothing about images at all, which is also
		// exactly what a host with no images would look like here.
		pairs = append(pairs, view.Pair{Key: "images", Value: "unreadable — " + imgErr.Message})
	}

	if !req.Bool("detail") {
		return view.KeyValue{Pairs: pairs}, nil
	}
	sections := []view.Section{
		{ID: "daemon", Title: "Daemon", View: view.KeyValue{Pairs: pairs}},
	}
	if len(sick) > 0 {
		sections = append(sections, view.Section{
			ID: "containers", Title: "Containers not running or not healthy",
			View: containerTable(sick)})
	} else {
		sections = append(sections, view.Section{
			ID: "containers", Title: "Containers",
			View: view.Text{Body: fmt.Sprintf("all %s running and healthy",
				count(len(rows), "container is", "containers are"))}})
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
	if imgErr != nil {
		// Sections carries what the page could not produce, for the reason
		// pkg/view states on the field itself: a degraded page that looks
		// complete is how a check comes back green.
		page.Warnings = append(page.Warnings, *imgErr)
	}
	return page, nil
}

// daemonAnswer is what the headline row says happened, in the four ways
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

// whatIsWrong is the word beside a container the overview lists as worth
// looking at. A container that is running and failing its healthcheck was
// listed as "web (running)" under "not running or not healthy": the state
// the daemon reports is the one thing that is fine about it, and the list
// read as saying the container was down.
func whatIsWrong(r containerRow) string {
	if strings.EqualFold(r.State, "running") {
		return "unhealthy"
	}
	return strings.ToLower(r.State)
}

func hintOf(e *view.Error, c connection) string {
	if e == nil || e.Hint == "" {
		return "`" + c.infoLine() + "` asks the same question directly"
	}
	return e.Hint
}

func truncate(items []string, max int) string {
	if len(items) <= max {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:max], ", ") + fmt.Sprintf(" and %d more", len(items)-max)
}

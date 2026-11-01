package family

import (
	"context"
	"database/sql"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

// withDB is the shape every capability here has: connect, or return the
// classified error; run; close.
//
// The close is deferred here rather than left to each handler because there
// are eight of them and the one that forgot would leak a connection against
// somebody else's server, which is the kind of bug that only shows up under
// load somebody else is carrying.
func withDB(ctx context.Context, req plugin.Request, fn func(context.Context, *sql.DB) (view.View, error)) (view.View, error) {
	db, verr := connect(ctx, req)
	if verr != nil {
		return nil, verr
	}
	defer func() { _ = db.Close() }()
	return fn(ctx, db)
}

// cap builds a capability with the shared connection inputs appended, so no
// declaration here can forget one and no two can disagree about a default.
//
// Every capability here is NoPreview because every one reaches off the box:
// the automatic dashboard runs Read capabilities unasked, and a live database
// somebody else depends on is not something this plugin gets to decide, on
// its own, is fine to poll every few seconds. An operator who has looked at
// their own deployment and decided otherwise still can — dashboard.tiles
// accepts any capability regardless of NoPreview, because naming one in a
// config file is the asking.
func cap(c plugin.Capability, own ...plugin.Field) plugin.Capability {
	c.Inputs = append(own, connFields()...)
	c.NoPreview = true
	return c
}

// Plugin is the plugin a binary serves: e, at the version that build was
// stamped with (main.version). It sets the engine this process serves.
func Plugin(e Engine, version string) plugin.Plugin {
	engine = e
	return plugin.Plugin{
		Name:    e.NS,
		Summary: e.Summary,
		Version: version,
		Capabilities: []plugin.Capability{
			overviewCapability(),
			statusCapability(),
			databaseListCapability(),
			tableListCapability(),
			schemaCapability(),
			queryCapability(),
			activityCapability(),
			replicationCapability(),
			dumpCapability(),
			restoreCapability(),
		},
	}
}

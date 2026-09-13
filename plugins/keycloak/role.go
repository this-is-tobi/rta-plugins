package main

import (
	"context"
	"sort"
	"strings"

	"github.com/this-is-tobi/rta/pkg/plugin"
	"github.com/this-is-tobi/rta/pkg/view"
)

func roleListCapability() plugin.Capability {
	return cap(plugin.Capability{
		ID:         "keycloak.role.list",
		Summary:    "The realm's roles, or one client's, and which are composites",
		Safety:     plugin.Read,
		Idempotent: true,
		Description: "Realm roles by default; --client names a client whose own roles to list " +
			"instead (realm-management is the one that holds every administrative role). A " +
			"composite role grants others when assigned, which is what makes it worth a column.",
		Run: runRoleList,
	},
		plugin.Field{Name: "client", Type: plugin.String, Default: "",
			Help: "list this client's roles instead of the realm's", Live: true, Suggest: suggestClients},
	)
}

func runRoleList(ctx context.Context, req plugin.Request) (view.View, error) {
	return withSession(ctx, req, func(ctx context.Context, s *session) (view.View, error) {
		path := "roles"
		if clientID := req.String("client"); clientID != "" {
			c, verr := s.client(ctx, clientID)
			if verr != nil {
				return nil, verr
			}
			path = "clients/" + segment(c.ID) + "/roles"
		}
		var roles []roleRep
		if verr := s.get(ctx, path, nil, &roles); verr != nil {
			return nil, verr
		}
		sort.Slice(roles, func(i, j int) bool { return roles[i].Name < roles[j].Name })
		t := columns(col("Role"), col("Composite"), col("Description"))
		for _, r := range roles {
			t.Rows = append(t.Rows, []string{r.Name, yesNo(r.Composite), description(r.Description)})
		}
		return finish(t), nil
	})
}

// description drops the console's translation keys: a built-in role
// describes itself as `${role_view-users}`, which is a key into a message
// bundle the console has and this table does not.
func description(s string) string {
	if strings.HasPrefix(s, "${") {
		return ""
	}
	return s
}

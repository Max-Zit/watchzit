package web

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/max-zit/watchzit/internal/store"
)

// Groups of a directory (Active Directory / LDAP or single sign-on) decide
// the role of its users at every sign-in.

// uiError is an error meant for the user: a sentence of the catalogs and
// its arguments, translated when it is shown.
type uiError struct {
	key  string
	args []any
}

func (e *uiError) Error() string { return fmt.Sprintf(e.key, e.args...) }

func uiErrf(key string, args ...any) error { return &uiError{key: key, args: args} }

// errText is an error in the user's language.
func (s *Server) errText(r *http.Request, err error) string {
	lang := s.lang(r, userFrom(r))
	if e, ok := err.(*uiError); ok {
		return translate(lang, e.key, e.args...)
	}
	if e, ok := err.(uiErr); ok {
		return translate(lang, string(e))
	}
	return translate(lang, sentence(err.Error()))
}

// GroupMapping maps directory groups to a role.
type GroupMapping struct {
	// Groups (names, DNs or IDs) per role; the highest role wins.
	AdminGroup    string `json:"admin_group,omitempty"`
	OperatorGroup string `json:"operator_group,omitempty"`
	ViewerGroup   string `json:"viewer_group,omitempty"`
	// DefaultRole for users in none of these groups ("" = they cannot
	// sign in).
	DefaultRole string `json:"default_role,omitempty"`
}

func (m *GroupMapping) validate() error {
	switch m.DefaultRole {
	case "", store.RoleOperator, store.RoleViewer:
	default:
		return uiErr("Unknown default role.")
	}
	if m.AdminGroup == "" && m.OperatorGroup == "" && m.ViewerGroup == "" && m.DefaultRole == "" {
		return uiErr("Map at least one group to a role, or choose a role for every user of the directory.")
	}
	return nil
}

func inGroups(groups []string, g string) bool {
	g = strings.TrimSpace(g)
	if g == "" {
		return false
	}
	for _, x := range groups {
		x = strings.TrimSpace(x)
		// A full DN matches, and so does its first name (CN=Admins,… ↔ Admins).
		if strings.EqualFold(x, g) || strings.EqualFold(cnOf(x), g) {
			return true
		}
	}
	return false
}

// cnOf is the first RDN value of a DN ("Admins" of "CN=Admins,OU=…").
func cnOf(dn string) string {
	first, _, _ := strings.Cut(dn, ",")
	if _, v, ok := strings.Cut(first, "="); ok {
		return strings.TrimSpace(v)
	}
	return dn
}

// role decides the role of a user with these groups.
func (m GroupMapping) role(groups []string) (string, error) {
	switch {
	case inGroups(groups, m.AdminGroup):
		return store.RoleAdmin, nil
	case inGroups(groups, m.OperatorGroup):
		return store.RoleOperator, nil
	case inGroups(groups, m.ViewerGroup):
		return store.RoleViewer, nil
	case m.DefaultRole != "":
		return m.DefaultRole, nil
	}
	return "", uiErr("Your directory account is in no group that may use WatchZit.")
}

// mappingFromForm reads the group fields shared by the LDAP and SSO pages.
func mappingFromForm(get func(string) string) GroupMapping {
	return GroupMapping{AdminGroup: strings.TrimSpace(get("admin_group")), OperatorGroup: strings.TrimSpace(get("operator_group")),
		ViewerGroup: strings.TrimSpace(get("viewer_group")), DefaultRole: get("default_role")}
}

// groupMappingData is the argument of the groupmapping template.
type groupMappingData struct {
	P *Page
	G GroupMapping
}

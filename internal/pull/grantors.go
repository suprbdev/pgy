package pull

import (
	"fmt"
	"strings"
)

// foreignGrants warns about the grants on the objects of the pulled
// schemas, and on those outside schemas, that a role other than the
// object's owner and the connecting role made (see diff.Live.ForeignGrants):
// grants blocks don't record grantors, so a database built from the YAML
// gets those privileges from the owner or the connecting role instead.
func (b *builder) foreignGrants() {
	var order []string
	byObject := map[string][]string{}
	for _, g := range b.live.ForeignGrants() {
		if g.Schema != "" && !b.wantSchema(g.Schema) {
			continue
		}
		object := g.Object
		if g.Column != "" {
			object = fmt.Sprintf("column %s of %s", g.Column, g.Object)
		}
		priv, option := strings.CutSuffix(g.Privilege, "*")
		if option {
			priv += " with grant option"
		}
		if byObject[object] == nil {
			order = append(order, object)
		}
		byObject[object] = append(byObject[object], fmt.Sprintf("%s's %s, granted by %s", g.Grantee, priv, g.Grantor))
	}
	for _, object := range order {
		b.warn("%s: %s; grants blocks don't record grantors, so a database built from this YAML gets these privileges granted by the owner or the connecting role", object, strings.Join(byObject[object], "; "))
	}
}

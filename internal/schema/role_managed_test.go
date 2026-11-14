package schema

import "testing"

func TestRoleManagedAttributesParse(t *testing.T) {
	db, err := parseFlexibleDatabase([]byte(`
roles:
  app: {login: false, inherit: true, connectionLimit: 3}
  bare: {}
`))
	if err != nil {
		t.Fatal(err)
	}
	app := db.Roles["app"]
	if !app.Managed["login"] || !app.Managed["inherit"] || !app.Managed["connectionLimit"] || app.Managed["createdb"] || app.ConnectionLimit != 3 {
		t.Errorf("unexpected managed set: %+v", app)
	}
	if len(db.Roles["bare"].Managed) != 0 {
		t.Errorf("bare role manages nothing: %+v", db.Roles["bare"].Managed)
	}
}

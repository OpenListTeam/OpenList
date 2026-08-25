package webdav

import (
	"encoding/xml"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupWebDAVPropertyDB(t *testing.T) *gorm.DB {
	t.Helper()
	conf.Conf = conf.DefaultConfig(t.TempDir())
	database, err := gorm.Open(sqlite.Open("file:webdav-property-test?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	db.Init(database)
	t.Cleanup(func() {
		sqlDB, err := database.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
	return database
}

func TestDeadPropsCopyAndDelete(t *testing.T) {
	database := setupWebDAVPropertyDB(t)
	rows := []model.WebDAVProperty{
		{Path: "/src", Namespace: "urn:test", Name: "root", InnerXML: []byte("<root/>")},
		{Path: "/src/child", Namespace: "urn:test", Name: "child", InnerXML: []byte("<child/>")},
		{Path: "/dst", Namespace: "urn:test", Name: "stale", InnerXML: []byte("<stale/>")},
		{Path: "/dst/old", Namespace: "urn:test", Name: "old", InnerXML: []byte("<old/>")},
		{Path: "/other", Namespace: "urn:test", Name: "keep", InnerXML: []byte("<keep/>")},
	}
	if err := database.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	if err := copyDeadProps("/src", "/dst"); err != nil {
		t.Fatal(err)
	}
	if _, ok := mustDeadProp(t, "/dst", "root"); !ok {
		t.Fatal("root property was not copied")
	}
	if _, ok := mustDeadProp(t, "/dst/child", "child"); !ok {
		t.Fatal("child property was not copied")
	}
	if _, ok := mustDeadProp(t, "/dst", "stale"); ok {
		t.Fatal("stale destination property was not replaced")
	}
	if _, ok := mustDeadProp(t, "/dst/old", "old"); ok {
		t.Fatal("stale destination subtree property was not removed")
	}
	if _, ok := mustDeadProp(t, "/src", "root"); !ok {
		t.Fatal("source property was changed by copy")
	}

	if err := deleteDeadProps("/dst"); err != nil {
		t.Fatal(err)
	}
	if props, err := getDeadProps("/dst"); err != nil {
		t.Fatal(err)
	} else if len(props) != 0 {
		t.Fatalf("destination properties remain after delete: %v", props)
	}
	if props, err := getDeadProps("/dst/child"); err != nil {
		t.Fatal(err)
	} else if len(props) != 0 {
		t.Fatalf("destination subtree properties remain after delete: %v", props)
	}
	if _, ok := mustDeadProp(t, "/other", "keep"); !ok {
		t.Fatal("delete removed an unrelated property")
	}
}

func mustDeadProp(t *testing.T, path, name string) (Property, bool) {
	t.Helper()
	props, err := getDeadProps(path)
	if err != nil {
		t.Fatal(err)
	}
	prop, ok := props[xmlName("urn:test", name)]
	return prop, ok
}

func xmlName(namespace, name string) xml.Name {
	return xml.Name{Space: namespace, Local: name}
}

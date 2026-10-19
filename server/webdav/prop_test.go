package webdav

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"strings"
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
	databaseName := strings.NewReplacer("/", "-", "\\", "-").Replace(t.Name())
	database, err := gorm.Open(sqlite.Open("file:"+databaseName+"?mode=memory&cache=shared"), &gorm.Config{})
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

	if err := withDeadPropsTransaction(context.Background(), func(tx *gorm.DB) error {
		return copyDeadProps(tx, "/src", "/dst")
	}); err != nil {
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

	if err := withDeadPropsTransaction(context.Background(), func(tx *gorm.DB) error {
		return deleteDeadProps(tx, "/dst")
	}); err != nil {
		t.Fatal(err)
	}
	if props, err := getDeadPropsInTest(t, "/dst"); err != nil {
		t.Fatal(err)
	} else if len(props) != 0 {
		t.Fatalf("destination properties remain after delete: %v", props)
	}
	if props, err := getDeadPropsInTest(t, "/dst/child"); err != nil {
		t.Fatal(err)
	} else if len(props) != 0 {
		t.Fatalf("destination subtree properties remain after delete: %v", props)
	}
	if _, ok := mustDeadProp(t, "/other", "keep"); !ok {
		t.Fatal("delete removed an unrelated property")
	}
}

func TestDeadPropsReadUsesTransaction(t *testing.T) {
	database := setupWebDAVPropertyDB(t)
	name := xmlName("urn:test", "value")
	if err := database.Create(&model.WebDAVProperty{Path: "/resource", Namespace: name.Space, Name: name.Local, InnerXML: []byte("<value>stored</value>")}).Error; err != nil {
		t.Fatal(err)
	}
	var gotNames []xml.Name
	var gotProps []Propstat
	err := withDeadPropsTransaction(context.Background(), func(tx *gorm.DB) error {
		var err error
		gotNames, err = propnames(tx, "/resource", &model.Object{Name: "resource"})
		if err != nil {
			return err
		}
		gotProps, err = props(tx, context.Background(), nil, "/resource", &model.Object{Name: "resource"}, []xml.Name{name})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, got := range gotNames {
		found = found || got == name
	}
	if !found {
		t.Fatalf("propnames = %v, want [%v]", gotNames, name)
	}
	if len(gotProps) != 1 || gotProps[0].Status != http.StatusOK || len(gotProps[0].Props) != 1 || string(gotProps[0].Props[0].InnerXML) != "<value>stored</value>" {
		t.Fatalf("props = %#v", gotProps)
	}
}

func TestProppatchCommitsDeadProperties(t *testing.T) {
	setupWebDAVPropertyDB(t)
	name := xmlName("urn:test", "value")
	pstats, err := patch(context.Background(), nil, "/resource", []Proppatch{{Props: []Property{{XMLName: name, Lang: "en", InnerXML: []byte("<value>patched</value>")}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(pstats) != 1 || pstats[0].Status != http.StatusOK {
		t.Fatalf("patch propstats = %#v", pstats)
	}
	prop, ok := mustDeadProp(t, "/resource", "value")
	if !ok || prop.Lang != "en" || string(prop.InnerXML) != "<value>patched</value>" {
		t.Fatalf("stored property = %#v, found=%v", prop, ok)
	}
}

func mustDeadProp(t *testing.T, path, name string) (Property, bool) {
	t.Helper()
	props, err := getDeadPropsInTest(t, path)
	if err != nil {
		t.Fatal(err)
	}
	prop, ok := props[xmlName("urn:test", name)]
	return prop, ok
}

func getDeadPropsInTest(t *testing.T, path string) (map[xml.Name]Property, error) {
	t.Helper()
	var props map[xml.Name]Property
	err := withDeadPropsTransaction(context.Background(), func(tx *gorm.DB) error {
		var err error
		props, err = getDeadProps(tx, path)
		return err
	})
	return props, err
}

func TestDeadPropsFileFailureRollsBackRowsAndFiles(t *testing.T) {
	database := setupWebDAVPropertyDB(t)
	if err := database.Create(&model.WebDAVProperty{Path: "/src", Namespace: "urn:test", Name: "p", InnerXML: []byte("<p/>")}).Error; err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"/dst": "old destination"}
	fileErr := errors.New("copy failed after creating destination")
	err := runDeadPropsFileMutation(context.Background(), func(tx *gorm.DB) error {
		return copyDeadProps(tx, "/src", "/dst")
	}, func() (*fileMutation, error) {
		files["/dst"] = "partial copy"
		return &fileMutation{rollback: func() error {
			files["/dst"] = "old destination"
			return nil
		}}, fileErr
	})
	if !errors.Is(err, fileErr) {
		t.Fatalf("mutation error = %v, want %v", err, fileErr)
	}
	if files["/dst"] != "old destination" {
		t.Fatalf("destination after rollback = %q", files["/dst"])
	}
	if _, ok := mustDeadProp(t, "/dst", "p"); ok {
		t.Fatal("destination property persisted after file failure")
	}
}

func TestDeleteFileFailureRollsBackPropertyDeletion(t *testing.T) {
	setupWebDAVPropertyDB(t)
	if err := db.GetDb().Create(&model.WebDAVProperty{Path: "/resource", Namespace: "urn:test", Name: "value", InnerXML: []byte("<value/>")}).Error; err != nil {
		t.Fatal(err)
	}
	fileErr := errors.New("remove failed")
	removeErr, transactionErr := removeResourceWithDeadProps(context.Background(), "/resource", func() error {
		return fileErr
	})
	if !errors.Is(removeErr, fileErr) || !errors.Is(transactionErr, fileErr) {
		t.Fatalf("remove errors = (%v, %v), want %v", removeErr, transactionErr, fileErr)
	}
	if _, ok := mustDeadProp(t, "/resource", "value"); !ok {
		t.Fatal("property was removed after the file deletion failed")
	}
}

func TestDeletePropertyWriteFailureDoesNotTouchFile(t *testing.T) {
	database := setupWebDAVPropertyDB(t)
	if err := database.Create(&model.WebDAVProperty{Path: "/resource", Namespace: "urn:test", Name: "value", InnerXML: []byte("<value/>")}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec("CREATE TRIGGER fail_webdav_property_delete BEFORE DELETE ON web_dav_properties BEGIN SELECT RAISE(ABORT, 'injected delete failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	fileTouched := false
	removeErr, transactionErr := removeResourceWithDeadProps(context.Background(), "/resource", func() error {
		fileTouched = true
		return nil
	})
	if transactionErr == nil {
		t.Fatal("expected property delete failure")
	}
	if removeErr != nil {
		t.Fatalf("remove error = %v, want property error only", removeErr)
	}
	if fileTouched {
		t.Fatal("file removal ran after property delete failed")
	}
	if _, ok := mustDeadProp(t, "/resource", "value"); !ok {
		t.Fatal("property was removed after database failure")
	}
}

func TestProppatchWriteFailureRollsBackEarlierChanges(t *testing.T) {
	database := setupWebDAVPropertyDB(t)
	if err := database.Create(&model.WebDAVProperty{Path: "/resource", Namespace: "urn:test", Name: "existing", InnerXML: []byte("<existing>old</existing>")}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec("CREATE TRIGGER fail_webdav_property_insert BEFORE INSERT ON web_dav_properties WHEN NEW.name = 'fail' BEGIN SELECT RAISE(ABORT, 'injected insert failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	_, err := patch(context.Background(), nil, "/resource", []Proppatch{{Props: []Property{
		{XMLName: xmlName("urn:test", "existing"), InnerXML: []byte("<existing>new</existing>")},
		{XMLName: xmlName("urn:test", "fail"), InnerXML: []byte("<fail/>")},
	}}})
	if err == nil {
		t.Fatal("expected property write failure")
	}
	prop, ok := mustDeadProp(t, "/resource", "existing")
	if !ok || string(prop.InnerXML) != "<existing>old</existing>" {
		t.Fatalf("existing property after rollback = %#v, found=%v", prop, ok)
	}
	if _, ok := mustDeadProp(t, "/resource", "fail"); ok {
		t.Fatal("failed property was persisted")
	}
}

func TestDeadPropsMutationDoesNotTouchFilesWhenDatabaseUnavailable(t *testing.T) {
	database := setupWebDAVPropertyDB(t)
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	fileTouched := false
	removeErr, transactionErr := removeResourceWithDeadProps(context.Background(), "/resource", func() error {
		fileTouched = true
		return nil
	})
	if transactionErr == nil {
		t.Fatal("expected unavailable database error")
	}
	if removeErr != nil {
		t.Fatalf("remove error = %v, want database error only", removeErr)
	}
	if fileTouched {
		t.Fatal("file mutation ran while the database was unavailable")
	}
}

func TestDeadPropsCommitFailureCompensatesFiles(t *testing.T) {
	setupWebDAVPropertyDB(t)
	files := map[string]string{"/dst": "old"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := runDeadPropsFileMutation(ctx, func(tx *gorm.DB) error {
		return tx.Create(&model.WebDAVProperty{Path: "/dst", Namespace: "urn:test", Name: "p", InnerXML: []byte("<p/>")}).Error
	}, func() (*fileMutation, error) {
		files["/dst"] = "new"
		cancel()
		return &fileMutation{rollback: func() error {
			files["/dst"] = "old"
			return nil
		}}, nil
	})
	if err == nil {
		t.Fatal("expected transaction commit error")
	}
	if files["/dst"] != "old" {
		t.Fatalf("destination after commit failure = %q", files["/dst"])
	}
	if _, ok := mustDeadProp(t, "/dst", "p"); ok {
		t.Fatal("property persisted after commit failure")
	}
}

func TestDeadPropsWriteFailureDoesNotTouchFiles(t *testing.T) {
	database := setupWebDAVPropertyDB(t)
	if err := database.Create(&model.WebDAVProperty{Path: "/src", Namespace: "urn:test", Name: "p", InnerXML: []byte("<p/>")}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Exec("CREATE TRIGGER fail_webdav_property_insert BEFORE INSERT ON web_dav_properties BEGIN SELECT RAISE(ABORT, 'injected insert failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	fileTouched := false
	err := runDeadPropsFileMutation(context.Background(), func(tx *gorm.DB) error {
		return copyDeadProps(tx, "/src", "/dst")
	}, func() (*fileMutation, error) {
		fileTouched = true
		return nil, nil
	})
	if err == nil {
		t.Fatal("expected property update error")
	}
	if fileTouched {
		t.Fatal("file mutation ran after database write failed")
	}
}

func TestDeadPropsCompensationFailureIsReported(t *testing.T) {
	setupWebDAVPropertyDB(t)
	fileErr := errors.New("copy failed")
	rollbackErr := errors.New("restore failed")
	err := runDeadPropsFileMutation(context.Background(), func(*gorm.DB) error { return nil }, func() (*fileMutation, error) {
		return &fileMutation{backupPaths: []string{"/dst/.openlist-backup-test"}, rollback: func() error { return rollbackErr }}, fileErr
	})
	if !errors.Is(err, fileErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("combined error = %v, want both source errors", err)
	}
}

func xmlName(namespace, name string) xml.Name {
	return xml.Name{Space: namespace, Local: name}
}

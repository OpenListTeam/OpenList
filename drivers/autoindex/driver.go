package autoindex

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/antchfx/htmlquery"
	"github.com/antchfx/xpath"
	"github.com/pkg/errors"
)

type Autoindex struct {
	model.Storage
	Addition
	itemXPath     *xpath.Expr
	nameXPath     *xpath.Expr
	modifiedXPath *xpath.Expr
	sizeXPath     *xpath.Expr
	ignores       map[string]any
}

func (d *Autoindex) Config() driver.Config {
	return config
}

func (d *Autoindex) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *Autoindex) Init(ctx context.Context) error {
	var err error
	d.itemXPath, err = xpath.Compile(d.ItemXPath)
	if err != nil {
		return errors.WithMessage(err, "failed to compile Item XPath")
	}
	d.nameXPath, err = xpath.Compile(d.NameXPath)
	if err != nil {
		return errors.WithMessage(err, "failed to compile Name XPath")
	}
	d.modifiedXPath, err = xpath.Compile(d.ModifiedXPath)
	if err != nil {
		return errors.WithMessage(err, "failed to compile Modified XPath")
	}
	d.sizeXPath, err = xpath.Compile(d.SizeXPath)
	if err != nil {
		return errors.WithMessage(err, "failed to compile Size XPath")
	}
	ignores := strings.Split(d.IgnoreFileNames, "\n")
	for _, i := range ignores {
		d.ignores[i] = struct{}{}
	}
	hasScheme := strings.Contains(d.URL, "://")
	hasSuffix := strings.HasSuffix(d.URL, "/")
	if !hasScheme || !hasSuffix {
		if !hasSuffix {
			d.URL = d.URL + "/"
		}
		if !hasScheme {
			d.URL = "https://" + d.URL
		}
		op.MustSaveDriverStorage(d)
	}
	return nil
}

func (d *Autoindex) Drop(ctx context.Context) error {
	return nil
}

func (d *Autoindex) GetRoot(ctx context.Context) (model.Obj, error) {
	return &model.Object{
		Name:     op.RootName,
		Path:     d.URL,
		Modified: d.Modified,
		Mask:     model.Locked,
		IsFolder: true,
	}, nil
}

func (d *Autoindex) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	res, err := http.Get(dir.GetPath())
	if err != nil {
		return nil, errors.WithMessagef(err, "failed to get url [%s]", dir.GetPath())
	}
	defer res.Body.Close()

	doc, err := htmlquery.Parse(res.Body)
	if err != nil {
		return nil, errors.WithMessagef(err, "failed to parse [%s]", dir.GetPath())
	}
	items := htmlquery.QuerySelectorAll(doc, d.itemXPath)
	objs := make([]model.Obj, 0, len(items))
	for _, item := range items {
		nameElem := htmlquery.QuerySelector(item, d.nameXPath)
		if nameElem == nil {
			continue
		}
		nameFull := htmlquery.InnerText(nameElem)
		name, isDir := strings.CutSuffix(nameFull, "/")
		if _, ok := d.ignores[name]; ok {
			continue
		}
		var size int64 = 0
		if sizeElem := htmlquery.QuerySelector(item, d.sizeXPath); sizeElem != nil {
			size, _ = strconv.ParseInt(htmlquery.InnerText(sizeElem), 10, 64)
		}
		var modified time.Time
		if modifiedElem := htmlquery.QuerySelector(item, d.modifiedXPath); modifiedElem != nil {
			modified, _ = time.Parse(d.ModifiedTimeFormat, htmlquery.InnerText(modifiedElem))
		}
		objs = append(objs, &model.Object{
			Name:     name,
			IsFolder: isDir,
			Path:     dir.GetPath() + nameFull,
			Modified: modified,
			Size:     size,
		})
	}
	return objs, nil
}

func (d *Autoindex) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	return &model.Link{
		URL: file.GetPath(),
	}, nil
}

var _ driver.Driver = (*Autoindex)(nil)

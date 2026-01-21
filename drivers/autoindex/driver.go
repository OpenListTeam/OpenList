package autoindex

import (
	"context"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/antchfx/htmlquery"
	"github.com/antchfx/xpath"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

type AutoIndex struct {
	model.Storage
	Addition
	itemXPath     *xpath.Expr
	nameXPath     *xpath.Expr
	modifiedXPath *xpath.Expr
	sizeXPath     *xpath.Expr
	ignores       map[string]any
}

func (d *AutoIndex) Config() driver.Config {
	return config
}

func (d *AutoIndex) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *AutoIndex) Init(ctx context.Context) error {
	var err error
	d.itemXPath, err = xpath.Compile(d.ItemXPath)
	if err != nil {
		return errors.WithMessage(err, "failed to compile Item XPath")
	}
	d.nameXPath, err = xpath.Compile(d.NameXPath)
	if err != nil {
		return errors.WithMessage(err, "failed to compile Name XPath")
	}
	if len(d.ModifiedXPath) > 0 {
		d.modifiedXPath, err = xpath.Compile(d.ModifiedXPath)
		if err != nil {
			return errors.WithMessage(err, "failed to compile Modified XPath")
		}
	}
	if len(d.SizeXPath) > 0 {
		d.sizeXPath, err = xpath.Compile(d.SizeXPath)
		if err != nil {
			return errors.WithMessage(err, "failed to compile Size XPath")
		}
	}
	ignores := strings.Split(d.IgnoreFileNames, "\n")
	d.ignores = make(map[string]any, len(ignores))
	for _, i := range ignores {
		i = strings.TrimSpace(i)
		if len(i) == 0 {
			continue
		}
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

func (d *AutoIndex) Drop(ctx context.Context) error {
	return nil
}

func (d *AutoIndex) GetRoot(ctx context.Context) (model.Obj, error) {
	return &model.Object{
		Name:     op.RootName,
		Path:     d.URL,
		Modified: d.Modified,
		Mask:     model.Locked,
		IsFolder: true,
	}, nil
}

func (d *AutoIndex) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	res, err := base.RestyClient.R().
		SetContext(ctx).
		SetDoNotParseResponse(true).
		Get(dir.GetPath())
	if err != nil {
		return nil, errors.WithMessagef(err, "failed to get url [%s]", dir.GetPath())
	}
	defer res.RawResponse.Body.Close()
	doc, err := htmlquery.Parse(res.RawBody())
	if err != nil {
		return nil, errors.WithMessagef(err, "failed to parse [%s]", dir.GetPath())
	}
	itemsIter := d.itemXPath.Select(htmlquery.CreateXPathNavigator(doc))
	var objs []model.Obj
	for itemsIter.MoveNext() {
		nameFull, err := parseString(d.nameXPath.Evaluate(itemsIter.Current().Copy()))
		if err != nil {
			log.Warnf("skip invalid name evaluating result: %v", err)
			continue
		}
		nameFull = strings.TrimSpace(nameFull)
		name, isDir := strings.CutSuffix(nameFull, "/")
		if _, ok := d.ignores[name]; ok {
			continue
		}
		var size int64 = 0
		exact := false
		modified := time.Now()
		if d.sizeXPath != nil {
			size, exact, err = parseSize(d.sizeXPath.Evaluate(itemsIter.Current().Copy()))
			if err != nil {
				log.Errorf("failed to parse size of %s: %v", name, err)
			}
		}
		if d.modifiedXPath != nil {
			modified, err = parseTime(d.modifiedXPath.Evaluate(itemsIter.Current().Copy()), d.ModifiedTimeFormat)
			if err != nil {
				log.Errorf("failed to parse modified time of %s: %v", name, err)
			}
		}
		var o model.Obj = &model.Object{
			Name:     name,
			IsFolder: isDir,
			Path:     dir.GetPath() + nameFull,
			Modified: modified,
			Size:     size,
		}
		if exact {
			o = &exactSizeObj{Obj: o}
		}
		objs = append(objs, o)
	}
	return objs, nil
}

func (d *AutoIndex) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if _, ok := file.(*exactSizeObj); ok || args.Redirect {
		return &model.Link{URL: file.GetPath()}, nil
	}
	res, err := base.RestyClient.R().
		SetContext(ctx).
		SetDoNotParseResponse(true).
		Head(file.GetPath())
	if err != nil {
		return nil, errors.WithMessagef(err, "failed to head [%s]", file.GetPath())
	}
	_ = res.RawResponse.Body.Close()
	return &model.Link{
		URL:           file.GetPath(),
		ContentLength: res.RawResponse.ContentLength,
	}, nil
}

var _ driver.Driver = (*AutoIndex)(nil)

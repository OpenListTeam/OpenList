package s3

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	log "github.com/sirupsen/logrus"
)

// do others that not defined in Driver interface

func (d *S3) initSession() error {
	accessKeyID, secretAccessKey, sessionToken := d.AccessKeyID, d.SecretAccessKey, d.SessionToken
	if d.config.Name == "Doge" {
		credentialsTmp, err := getCredentials(d.AccessKeyID, d.SecretAccessKey)
		if err != nil {
			return err
		}
		accessKeyID, secretAccessKey, sessionToken = credentialsTmp.AccessKeyId, credentialsTmp.SecretAccessKey, credentialsTmp.SessionToken
	}
	d.cfg = aws.Config{
		Credentials: credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, sessionToken),
		Region:      d.Region,
	}
	return nil
}

const (
	ClientTypeNormal = iota
	ClientTypeLink
	ClientTypeDirectUpload
)

func (d *S3) getClient(clientType int) *s3.Client {
	return s3.NewFromConfig(d.cfg, func(o *s3.Options) {
		o.UsePathStyle = d.ForcePathStyle
		o.BaseEndpoint = aws.String(d.Endpoint)
		o.APIOptions = append(o.APIOptions, func(stack *middleware.Stack) error {
			// User-Agent middleware
			if d.UserAgent != "" {
				if err := stack.Build.Add(middleware.BuildMiddlewareFunc("SetUserAgent",
					func(ctx context.Context, in middleware.BuildInput, next middleware.BuildHandler) (middleware.BuildOutput, middleware.Metadata, error) {
						req, ok := in.Request.(*smithyhttp.Request)
						if !ok {
							return next.HandleBuild(ctx, in)
						}
						req.Header.Set("User-Agent", d.UserAgent)
						return next.HandleBuild(ctx, in)
					},
				), middleware.After); err != nil {
					return err
				}
			}
			// CustomHost middleware
			if clientType == ClientTypeLink && d.CustomHost != "" {
				if err := stack.Build.Add(middleware.BuildMiddlewareFunc("SetCustomHost",
					func(ctx context.Context, in middleware.BuildInput, next middleware.BuildHandler) (middleware.BuildOutput, middleware.Metadata, error) {
						req, ok := in.Request.(*smithyhttp.Request)
						if !ok {
							return next.HandleBuild(ctx, in)
						}
						if req.Method == http.MethodGet {
							split := strings.SplitN(d.CustomHost, "://", 2)
							if len(split) > 1 && utils.SliceContains([]string{"http", "https"}, split[0]) {
								req.URL.Scheme = split[0]
								req.URL.Host = split[1]
							} else {
								req.URL.Host = d.CustomHost
							}
						}
						return next.HandleBuild(ctx, in)
					},
				), middleware.After); err != nil {
					return err
				}
			}
			// DirectUploadHost middleware
			if clientType == ClientTypeDirectUpload && d.DirectUploadHost != "" {
				if err := stack.Build.Add(middleware.BuildMiddlewareFunc("SetDirectUploadHost",
					func(ctx context.Context, in middleware.BuildInput, next middleware.BuildHandler) (middleware.BuildOutput, middleware.Metadata, error) {
						req, ok := in.Request.(*smithyhttp.Request)
						if !ok {
							return next.HandleBuild(ctx, in)
						}
						if req.Method == http.MethodPut {
							split := strings.SplitN(d.DirectUploadHost, "://", 2)
							if len(split) > 1 && utils.SliceContains([]string{"http", "https"}, split[0]) {
								req.URL.Scheme = split[0]
								req.URL.Host = split[1]
							} else {
								req.URL.Host = d.DirectUploadHost
							}
						}
						return next.HandleBuild(ctx, in)
					},
				), middleware.After); err != nil {
					return err
				}
			}
			return nil
		})
	})
}

func getKey(path string, dir bool) string {
	path = strings.TrimPrefix(path, "/")
	if path != "" && dir {
		path += "/"
	}
	return path
}

var defaultPlaceholderName = ".openlist"

func getPlaceholderName(placeholder string) string {
	if placeholder == "" {
		return defaultPlaceholderName
	}
	return placeholder
}

func (d *S3) listV1(ctx context.Context, dirPath string, args model.ListArgs) ([]model.Obj, error) {
	prefix := getKey(dirPath, true)
	log.Debugf("list: %s", prefix)
	files := make([]model.Obj, 0)
	marker := ""
	for {
		input := &s3.ListObjectsInput{
			Bucket:    &d.Bucket,
			Marker:    &marker,
			Prefix:    &prefix,
			Delimiter: aws.String("/"),
		}
		listObjectsResult, err := d.client.ListObjects(ctx, input)
		if err != nil {
			return nil, err
		}
		for _, object := range listObjectsResult.CommonPrefixes {
			name := path.Base(strings.Trim(*object.Prefix, "/"))
			file := model.Object{
				Path:     path.Join(dirPath, name),
				Name:     name,
				Modified: d.Modified,
				IsFolder: true,
			}
			files = append(files, &file)
		}
		for _, object := range listObjectsResult.Contents {
			name := path.Base(*object.Key)
			if !args.S3ShowPlaceholder && (name == getPlaceholderName(d.Placeholder) || name == d.Placeholder) {
				continue
			}
			file := model.Object{
				Path:     path.Join(dirPath, name),
				Name:     name,
				Size:     *object.Size,
				Modified: *object.LastModified,
			}
			files = append(files, &file)
		}
		if listObjectsResult.IsTruncated == nil {
			return nil, errors.New("IsTruncated nil")
		}
		if *listObjectsResult.IsTruncated {
			marker = *listObjectsResult.NextMarker
		} else {
			break
		}
	}
	return files, nil
}

func (d *S3) listV2(ctx context.Context, dirPath string, args model.ListArgs) ([]model.Obj, error) {
	prefix := getKey(dirPath, true)
	files := make([]model.Obj, 0)
	var continuationToken, startAfter *string
	for {
		input := &s3.ListObjectsV2Input{
			Bucket:            &d.Bucket,
			ContinuationToken: continuationToken,
			Prefix:            &prefix,
			Delimiter:         aws.String("/"),
			StartAfter:        startAfter,
		}
		listObjectsResult, err := d.client.ListObjectsV2(ctx, input)
		if err != nil {
			return nil, err
		}
		log.Debugf("resp: %+v", listObjectsResult)
		for _, object := range listObjectsResult.CommonPrefixes {
			name := path.Base(strings.Trim(*object.Prefix, "/"))
			file := model.Object{
				Path:     path.Join(dirPath, name),
				Name:     name,
				Modified: d.Modified,
				IsFolder: true,
			}
			files = append(files, &file)
		}
		for _, object := range listObjectsResult.Contents {
			if strings.HasSuffix(*object.Key, "/") {
				continue
			}
			name := path.Base(*object.Key)
			if !args.S3ShowPlaceholder && (name == getPlaceholderName(d.Placeholder) || name == d.Placeholder) {
				continue
			}
			file := model.Object{
				Path:     path.Join(dirPath, name),
				Name:     name,
				Size:     *object.Size,
				Modified: *object.LastModified,
			}
			files = append(files, &file)
		}
		if !aws.ToBool(listObjectsResult.IsTruncated) {
			break
		}
		if listObjectsResult.NextContinuationToken != nil {
			continuationToken = listObjectsResult.NextContinuationToken
			continue
		}
		if len(listObjectsResult.Contents) == 0 {
			break
		}
		startAfter = listObjectsResult.Contents[len(listObjectsResult.Contents)-1].Key
	}
	return files, nil
}

func (d *S3) copy(ctx context.Context, src string, dst string, isDir bool) error {
	if isDir {
		return d.copyDir(ctx, src, dst)
	}
	return d.copyFile(ctx, src, dst)
}

func (d *S3) copyFile(ctx context.Context, src string, dst string) error {
	srcKey := getKey(src, false)
	dstKey := getKey(dst, false)
	encodedKey := strings.ReplaceAll(url.PathEscape(d.Bucket+"/"+srcKey), "+", "%2B")
	input := &s3.CopyObjectInput{
		Bucket:     &d.Bucket,
		CopySource: aws.String(encodedKey),
		Key:        &dstKey,
	}
	_, err := d.client.CopyObject(ctx, input)
	return err
}

func (d *S3) copyDir(ctx context.Context, src string, dst string) error {
	objs, err := op.List(ctx, d, src, model.ListArgs{S3ShowPlaceholder: true})
	if err != nil {
		return err
	}
	for _, obj := range objs {
		cSrc := path.Join(src, obj.GetName())
		cDst := path.Join(dst, obj.GetName())
		if obj.IsDir() {
			err = d.copyDir(ctx, cSrc, cDst)
		} else {
			err = d.copyFile(ctx, cSrc, cDst)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (d *S3) removeDir(ctx context.Context, src string) error {
	objs, err := op.List(ctx, d, src, model.ListArgs{})
	if err != nil {
		return err
	}
	for _, obj := range objs {
		cSrc := path.Join(src, obj.GetName())
		if obj.IsDir() {
			err = d.removeDir(ctx, cSrc)
		} else {
			err = d.removeFile(ctx, cSrc)
		}
		if err != nil {
			return err
		}
	}
	_ = d.removeFile(ctx, path.Join(src, getPlaceholderName(d.Placeholder)))
	_ = d.removeFile(ctx, path.Join(src, d.Placeholder))
	return nil
}

func (d *S3) removeFile(ctx context.Context, src string) error {
	key := getKey(src, false)
	input := &s3.DeleteObjectInput{
		Bucket: &d.Bucket,
		Key:    &key,
	}
	_, err := d.client.DeleteObject(ctx, input)
	return err
}

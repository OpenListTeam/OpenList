package degoo

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

func (d *Degoo) getBucketWriteAuth4(ctx context.Context, file model.FileStreamer, parentID string, checksum string) (*DegooGetBucketWriteAuth4Data, error) {
	const query = `query GetBucketWriteAuth4(
    $Token: String!
    $ParentID: String!
    $StorageUploadInfos: [StorageUploadInfo2]
  ) {
    getBucketWriteAuth4(
      Token: $Token
      ParentID: $ParentID
      StorageUploadInfos: $StorageUploadInfos
    ) {
      AuthData {
        PolicyBase64
        Signature
        BaseURL
        KeyPrefix
        AccessKey {
          Key
          Value
        }
        ACL
        AdditionalBody {
          Key
          Value
        }
      }
      Error
    }
  }`

	variables := map[string]interface{}{
		"Token":    d.AccessToken,
		"ParentID": parentID,
		"StorageUploadInfos": []map[string]string{{
			"FileName": file.GetName(),
			"Checksum": checksum,
			"Size":     strconv.FormatInt(file.GetSize(), 10),
		}}}

	data, err := d.apiCall(ctx, "GetBucketWriteAuth4", query, variables)
	if err != nil {
		return nil, err
	}

	var resp DegooGetBucketWriteAuth4Data
	err = json.Unmarshal(data, &resp)
	if err != nil {
		return nil, err
	}

	return &resp, nil
}

// checkSum calculates the SHA1-based checksum for Degoo upload API.
func (d *Degoo) checkSum(file io.Reader) (string, error) {
	seed := []byte{13, 7, 2, 2, 15, 40, 75, 117, 13, 10, 19, 16, 29, 23, 3, 36}
	hasher := sha1.New()
	hasher.Write(seed)

	if _, err := utils.CopyWithBuffer(hasher, file); err != nil {
		return "", err
	}

	cs := hasher.Sum(nil)

	csBytes := []byte{10, byte(len(cs))}
	csBytes = append(csBytes, cs...)
	csBytes = append(csBytes, 16, 0)

	return strings.ReplaceAll(base64.StdEncoding.EncodeToString(csBytes), "/", "_"), nil
}

func (d *Degoo) uploadS3(ctx context.Context, auths *DegooGetBucketWriteAuth4Data, tmpF model.File, file model.FileStreamer, checksum string) error {
	a := auths.GetBucketWriteAuth4[0].AuthData

	_, err := tmpF.Seek(0, io.SeekStart)
	if err != nil {
		return err
	}

	ext := utils.Ext(file.GetName())
	key := fmt.Sprintf("%s%s/%s.%s", a.KeyPrefix, ext, checksum, ext)

	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	err = w.WriteField("key", key)
	if err != nil {
		return err
	}
	err = w.WriteField("acl", a.ACL)
	if err != nil {
		return err
	}
	err = w.WriteField("policy", a.PolicyBase64)
	if err != nil {
		return err
	}
	err = w.WriteField("signature", a.Signature)
	if err != nil {
		return err
	}
	err = w.WriteField(a.AccessKey.Key, a.AccessKey.Value)
	if err != nil {
		return err
	}
	for _, additional := range a.AdditionalBody {
		err = w.WriteField(additional.Key, additional.Value)
		if err != nil {
			return err
		}
	}
	err = w.WriteField("Content-Type", "")
	if err != nil {
		return err
	}

	_, err = w.CreateFormFile("file", key)
	if err != nil {
		return err
	}

	headSize := b.Len()
	err = w.Close()
	if err != nil {
		return err
	}
	head := bytes.NewReader(b.Bytes()[:headSize])
	tail := bytes.NewReader(b.Bytes()[headSize:])

	rateLimitedRd := driver.NewLimitedUploadStream(ctx, io.MultiReader(head, tmpF, tail))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL, rateLimitedRd)
	if err != nil {
		return err
	}
	req.Header.Add("ngsw-bypass", "1")
	req.Header.Add("Content-Type", w.FormDataContentType())

	res, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		return fmt.Errorf("upload failed with status code %d", res.StatusCode)
	}
	return nil
}

var _ driver.Driver = (*Degoo)(nil)

func (d *Degoo) SetUploadFile3(ctx context.Context, file model.FileStreamer, parentID string, checksum string) (*DegooSetUploadFile3Data, error) {
	const query = `mutation SetUploadFile3($Token: String!, $FileInfos: [FileInfoUpload3]!) {
    setUploadFile3(Token: $Token, FileInfos: $FileInfos)
  }`

	variables := map[string]interface{}{
		"Token": d.AccessToken,
		"FileInfos": []map[string]string{{
			"Checksum":     checksum,
			"CreationTime": strconv.FormatInt(file.CreateTime().UnixMilli(), 10),
			"Name":         file.GetName(),
			"ParentID":     parentID,
			"Size":         strconv.FormatInt(file.GetSize(), 10),
		}}}

	data, err := d.apiCall(ctx, "SetUploadFile3", query, variables)
	if err != nil {
		return nil, err
	}

	var resp DegooSetUploadFile3Data
	err = json.Unmarshal(data, &resp)
	if err != nil {
		return nil, err
	}

	return &resp, nil
}

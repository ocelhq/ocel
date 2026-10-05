package bucket

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

const answerReadCapBytes = 1 << 20

type multipart struct {
	endpoint string
	client   *http.Client
}

type initiatedUpload struct {
	UploadID string `xml:"UploadId"`
}

type completedUpload struct {
	XMLName xml.Name        `xml:"CompleteMultipartUpload"`
	Parts   []completedPart `xml:"Part"`
}

type completedPart struct {
	PartNumber int32  `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

type answeredError struct {
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

func (m multipart) objectURL(bucket, key string, query url.Values) string {
	at := &url.URL{Path: "/" + bucket + "/" + key, RawQuery: query.Encode()}
	return strings.TrimRight(m.endpoint, "/") + at.EscapedPath() + "?" + strings.TrimSuffix(at.RawQuery, "=")
}

func (m multipart) open(ctx context.Context, bucket, key, contentType, cacheControl string, metadata map[string]string) (string, error) {
	header := http.Header{}
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	if cacheControl != "" {
		header.Set("Cache-Control", cacheControl)
	}
	for name, value := range metadata {
		header.Set(metadataHeader+name, value)
	}
	answer, err := m.ask(ctx, http.MethodPost, m.objectURL(bucket, key, url.Values{"uploads": {""}}), header, nil)
	if err != nil {
		return "", err
	}
	var opened initiatedUpload
	if err := xml.Unmarshal(answer, &opened); err != nil || opened.UploadID == "" {
		return "", fmt.Errorf("open a multipart upload of %s: the answer names no upload id: %s", key, answer)
	}
	return opened.UploadID, nil
}

func (m multipart) assemble(ctx context.Context, bucket, key, uploadID string, parts []s3types.CompletedPart, generationMatch string) error {
	listed := completedUpload{}
	for _, part := range parts {
		listed.Parts = append(listed.Parts, completedPart{PartNumber: aws.ToInt32(part.PartNumber), ETag: aws.ToString(part.ETag)})
	}
	body, err := xml.Marshal(listed)
	if err != nil {
		return err
	}
	header := http.Header{"Content-Type": {"application/xml"}}
	if generationMatch != "" {
		header.Set("x-goog-if-generation-match", generationMatch)
	}
	answer, err := m.ask(ctx, http.MethodPost, m.objectURL(bucket, key, url.Values{"uploadId": {uploadID}}), header, body)
	if err != nil {
		return err
	}
	var refused answeredError
	if xml.Unmarshal(answer, &refused) == nil && refused.Code != "" {
		return &smithy.GenericAPIError{Code: refused.Code, Message: refused.Message}
	}
	return nil
}

func (m multipart) abort(ctx context.Context, bucket, key, uploadID string) error {
	_, err := m.ask(ctx, http.MethodDelete, m.objectURL(bucket, key, url.Values{"uploadId": {uploadID}}), nil, nil)
	return err
}

func (m multipart) ask(ctx context.Context, method, target string, header http.Header, body []byte) ([]byte, error) {
	return retried(ctx, func() ([]byte, int, error) {
		req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		for name, values := range header {
			req.Header[name] = values
		}
		resp, err := m.client.Do(req)
		if err != nil {
			return nil, 0, err
		}
		defer resp.Body.Close()
		answer, err := io.ReadAll(io.LimitReader(resp.Body, answerReadCapBytes))
		if err != nil {
			return nil, resp.StatusCode, err
		}
		if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
			return answer, resp.StatusCode, nil
		}
		return nil, resp.StatusCode, refusedAnswer(resp.StatusCode, answer)
	})
}

func refusedAnswer(status int, answer []byte) error {
	var refused answeredError
	_ = xml.Unmarshal(answer, &refused)
	if code, known := s3Codes[status]; known {
		return &smithy.GenericAPIError{Code: code, Message: refused.Message}
	}
	code := refused.Code
	if code == "" {
		code = http.StatusText(status)
	}
	return &smithy.GenericAPIError{Code: code, Message: fmt.Sprintf("Cloud Storage answered %d: %s", status, refused.Message)}
}
